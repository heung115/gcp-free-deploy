package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var (
	projectIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	zonePattern        = regexp.MustCompile(`^[a-z]+(?:-[a-z]+)*[0-9]+-[a-z]$`)
	dockerImagePattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]{1,5})?/)?(?:[a-z0-9]+(?:[._-][a-z0-9]+)*/)*[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[a-fA-F0-9]{64})?$`)
	githubPathPattern  = regexp.MustCompile(`^/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\.git)?$`)
)

// DeployConfig is the complete, validated input to one ephemeral deployment.
type DeployConfig struct {
	ProjectID           string   `json:"project_id"`
	Zone                string   `json:"zone"`
	FallbackZones       []string `json:"fallback_zones,omitempty"`
	Source              string   `json:"source"`
	DockerImage         string   `json:"docker_image,omitempty"`
	GithubRepo          string   `json:"github_repo,omitempty"`
	ContainerPort       int      `json:"container_port"`
	AllowedSourceRanges []string `json:"allowed_source_ranges"`
	MachineType         string   `json:"machine_type,omitempty"`
	DiskSizeGB          int      `json:"disk_size_gb,omitempty"`
}

// ValidationError identifies the invalid field without echoing its value.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Normalize removes harmless surrounding whitespace and normalizes enum values.
func (c *DeployConfig) Normalize() {
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	c.Zone = strings.TrimSpace(c.Zone)
	for i := range c.FallbackZones {
		c.FallbackZones[i] = strings.TrimSpace(c.FallbackZones[i])
	}
	c.Source = strings.ToLower(strings.TrimSpace(c.Source))
	c.DockerImage = strings.TrimSpace(c.DockerImage)
	c.GithubRepo = strings.TrimSpace(c.GithubRepo)
	c.MachineType = strings.TrimSpace(c.MachineType)
	if c.MachineType == "" {
		c.MachineType = "e2-micro"
	}
	if c.DiskSizeGB == 0 {
		c.DiskSizeGB = 10
	}
	for i := range c.AllowedSourceRanges {
		c.AllowedSourceRanges[i] = strings.TrimSpace(c.AllowedSourceRanges[i])
	}
}

// Validate rejects values that are ambiguous or unsafe to pass to Terraform.
func (c DeployConfig) Validate() error {
	if !projectIDPattern.MatchString(c.ProjectID) {
		return &ValidationError{Field: "project_id", Message: "6~30자의 소문자·숫자·하이픈 형식이어야 합니다"}
	}
	if !zonePattern.MatchString(c.Zone) {
		return &ValidationError{Field: "zone", Message: "올바른 GCP zone 형식이어야 합니다 (예: us-central1-a)"}
	}
	primaryRegion := regionFromZone(c.Zone)
	seenZones := map[string]bool{c.Zone: true}
	for _, zone := range c.FallbackZones {
		if !zonePattern.MatchString(zone) {
			return &ValidationError{Field: "fallback_zones", Message: "각 항목은 올바른 GCP zone 형식이어야 합니다"}
		}
		if regionFromZone(zone) != primaryRegion {
			return &ValidationError{Field: "fallback_zones", Message: "전용 subnet을 유지하려면 기본 zone과 같은 region이어야 합니다"}
		}
		if seenZones[zone] {
			return &ValidationError{Field: "fallback_zones", Message: "중복 zone을 포함할 수 없습니다"}
		}
		seenZones[zone] = true
	}
	if c.ContainerPort < 1 || c.ContainerPort > 65535 {
		return &ValidationError{Field: "container_port", Message: "1~65535 범위여야 합니다"}
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`).MatchString(c.MachineType) {
		return &ValidationError{Field: "machine_type", Message: "올바른 machine type 이름이어야 합니다"}
	}
	if c.DiskSizeGB < 10 || c.DiskSizeGB > 30 {
		return &ValidationError{Field: "disk_size_gb", Message: "10~30GB 범위여야 합니다"}
	}

	switch c.Source {
	case "docker":
		if !dockerImagePattern.MatchString(c.DockerImage) {
			return &ValidationError{Field: "docker_image", Message: "지원하는 Docker 이미지 참조 형식이 아닙니다"}
		}
		if !dockerImageIsPinned(c.DockerImage) {
			return &ValidationError{Field: "docker_image", Message: "명시적인 tag 또는 sha256 digest가 필요합니다"}
		}
		if c.GithubRepo != "" {
			return &ValidationError{Field: "github_repo", Message: "source가 docker일 때는 비워야 합니다"}
		}
	case "github":
		if err := validateGitHubURL(c.GithubRepo); err != nil {
			return err
		}
		if c.DockerImage != "" {
			return &ValidationError{Field: "docker_image", Message: "source가 github일 때는 비워야 합니다"}
		}
	default:
		return &ValidationError{Field: "source", Message: "docker 또는 github여야 합니다"}
	}

	if len(c.AllowedSourceRanges) == 0 {
		return &ValidationError{Field: "allowed_source_ranges", Message: "HTTP 접근을 허용할 IPv4 CIDR을 하나 이상 명시해야 합니다"}
	}
	for _, sourceRange := range c.AllowedSourceRanges {
		ip, _, err := net.ParseCIDR(sourceRange)
		if err != nil || ip.To4() == nil {
			return &ValidationError{Field: "allowed_source_ranges", Message: "각 항목은 올바른 IPv4 CIDR이어야 합니다"}
		}
	}

	return nil
}

func dockerImageIsPinned(image string) bool {
	if regexp.MustCompile(`@sha256:[a-fA-F0-9]{64}$`).MatchString(image) {
		return true
	}
	lastSegment := image
	if slash := strings.LastIndex(lastSegment, "/"); slash >= 0 {
		lastSegment = lastSegment[slash+1:]
	}
	colon := strings.LastIndex(lastSegment, ":")
	return colon > 0 && colon < len(lastSegment)-1
}

// LoadDeployConfig decodes exactly one JSON object and rejects unknown fields.
func LoadDeployConfig(path string) (DeployConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return DeployConfig{}, fmt.Errorf("설정 파일 열기: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var cfg DeployConfig
	if err := decoder.Decode(&cfg); err != nil {
		return DeployConfig{}, fmt.Errorf("설정 파일 JSON 파싱: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return DeployConfig{}, fmt.Errorf("설정 파일에는 JSON 객체 하나만 있어야 합니다")
		}
		return DeployConfig{}, fmt.Errorf("설정 파일의 후행 데이터: %w", err)
	}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return DeployConfig{}, err
	}
	return cfg, nil
}

func validateGitHubURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !githubPathPattern.MatchString(u.EscapedPath()) {
		return &ValidationError{Field: "github_repo", Message: "https://github.com/<owner>/<repo>[.git] 형식이어야 합니다"}
	}
	return nil
}

func (c DeployConfig) exposesHTTPToEveryone() bool {
	for _, sourceRange := range c.AllowedSourceRanges {
		ip, network, err := net.ParseCIDR(sourceRange)
		if err == nil && ip.To4() != nil {
			ones, bits := network.Mask.Size()
			if ones == 0 && bits == 32 {
				return true
			}
		}
		if sourceRange == "0.0.0.0/0" {
			return true
		}
	}
	return false
}

func regionFromZone(zone string) string {
	if index := strings.LastIndex(zone, "-"); index > 0 {
		return zone[:index]
	}
	return ""
}
