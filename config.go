package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
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
	if repositoryURL, err := url.Parse(c.GithubRepo); err == nil {
		if strings.EqualFold(repositoryURL.Scheme, "https") {
			repositoryURL.Scheme = "https"
		}
		if strings.EqualFold(repositoryURL.Hostname(), "github.com") && repositoryURL.Port() == "" {
			repositoryURL.Host = "github.com"
		}
		c.GithubRepo = repositoryURL.String()
	}
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
		return &ValidationError{Field: "project_id", Message: "must be 6-30 characters, start with a lowercase letter, end with a lowercase letter or digit, and contain only lowercase letters, digits, and hyphens"}
	}
	if !zonePattern.MatchString(c.Zone) {
		return &ValidationError{Field: "zone", Message: "must be a valid GCP zone (for example, us-central1-a)"}
	}
	primaryRegion := regionFromZone(c.Zone)
	seenZones := map[string]bool{c.Zone: true}
	for _, zone := range c.FallbackZones {
		if !zonePattern.MatchString(zone) {
			return &ValidationError{Field: "fallback_zones", Message: "each entry must be a valid GCP zone"}
		}
		if regionFromZone(zone) != primaryRegion {
			return &ValidationError{Field: "fallback_zones", Message: "each entry must be in the same region as the primary zone to keep the dedicated subnet"}
		}
		if seenZones[zone] {
			return &ValidationError{Field: "fallback_zones", Message: "must not contain duplicate zones"}
		}
		seenZones[zone] = true
	}
	if c.ContainerPort < 1 || c.ContainerPort > 65535 {
		return &ValidationError{Field: "container_port", Message: "must be between 1 and 65535"}
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`).MatchString(c.MachineType) {
		return &ValidationError{Field: "machine_type", Message: "must be a valid machine type name"}
	}
	if c.DiskSizeGB < 10 || c.DiskSizeGB > 30 {
		return &ValidationError{Field: "disk_size_gb", Message: "must be between 10 and 30 GB"}
	}

	switch c.Source {
	case "docker":
		if !dockerImagePattern.MatchString(c.DockerImage) {
			return &ValidationError{Field: "docker_image", Message: "has an unsupported Docker image reference format"}
		}
		if !dockerImageIsPinned(c.DockerImage) {
			return &ValidationError{Field: "docker_image", Message: "requires an explicit tag or sha256 digest"}
		}
		if c.GithubRepo != "" {
			return &ValidationError{Field: "github_repo", Message: "must be empty when source is docker"}
		}
	case "github":
		if err := validateGitHubURL(c.GithubRepo); err != nil {
			return err
		}
		if c.DockerImage != "" {
			return &ValidationError{Field: "docker_image", Message: "must be empty when source is github"}
		}
	default:
		return &ValidationError{Field: "source", Message: "must be docker or github"}
	}

	if len(c.AllowedSourceRanges) == 0 {
		return &ValidationError{Field: "allowed_source_ranges", Message: "must specify at least one IPv4 CIDR for HTTP access"}
	}
	for _, sourceRange := range c.AllowedSourceRanges {
		ip, network, err := net.ParseCIDR(sourceRange)
		bits := 0
		if err == nil {
			_, bits = network.Mask.Size()
		}
		if err != nil || ip.To4() == nil || bits != 32 {
			return &ValidationError{Field: "allowed_source_ranges", Message: "each entry must be a valid IPv4 CIDR"}
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
		return DeployConfig{}, fmt.Errorf("open config file: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var cfg DeployConfig
	if err := decoder.Decode(&cfg); err != nil {
		return DeployConfig{}, fmt.Errorf("parse config file JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return DeployConfig{}, fmt.Errorf("config file must contain exactly one JSON object")
		}
		return DeployConfig{}, fmt.Errorf("trailing data in config file: %w", err)
	}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return DeployConfig{}, err
	}
	return cfg, nil
}

func validateGitHubURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "github.com" || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !githubPathPattern.MatchString(u.EscapedPath()) {
		return &ValidationError{Field: "github_repo", Message: "must match https://github.com/<owner>/<repo>[.git]"}
	}
	return nil
}

func (c DeployConfig) exposesHTTPToEveryone() bool {
	type addressRange struct {
		start uint32
		end   uint32
	}
	ranges := make([]addressRange, 0, len(c.AllowedSourceRanges))
	for _, sourceRange := range c.AllowedSourceRanges {
		ip, network, err := net.ParseCIDR(sourceRange)
		if err != nil || ip.To4() == nil {
			continue
		}
		ones, bits := network.Mask.Size()
		if bits != 32 {
			continue
		}
		mask := uint32(0)
		if ones > 0 {
			mask = ^uint32(0) << (32 - ones)
		}
		start := binary.BigEndian.Uint32(ip.To4()) & mask
		ranges = append(ranges, addressRange{start: start, end: start | ^mask})
	}
	if len(ranges) == 0 {
		return false
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	if ranges[0].start != 0 {
		return false
	}
	coveredThrough := uint64(ranges[0].end)
	for _, current := range ranges[1:] {
		if uint64(current.start) > coveredThrough+1 {
			return false
		}
		if uint64(current.end) > coveredThrough {
			coveredThrough = uint64(current.end)
		}
	}
	return coveredThrough == uint64(^uint32(0))
}

func regionFromZone(zone string) string {
	if index := strings.LastIndex(zone, "-"); index > 0 {
		return zone[:index]
	}
	return ""
}
