package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const maxDiagnosticBytes = 8192

const terraformVariablesName = ".gcp-free-deploy.tfvars.json"

var diagnosticSecretPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|basic)\s+)[^\s]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)(set-cookie\s*:\s*)[^\r\n]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)(["']?[A-Za-z0-9_.-]*(?:access[_-]?token|refresh[_-]?token|client[_-]?secret|private[_-]?key|password|passwd|api[_-]?key|secret|credential|cookie|session)[A-Za-z0-9_.-]*["']?\s*[:=]\s*["']?)[^"'\s,}]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?:github_pat_[A-Za-z0-9_]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|AIza[0-9A-Za-z_-]{30,}|AKIA[0-9A-Z]{16})`), `[REDACTED TOKEN]`},
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), `[REDACTED PRIVATE KEY]`},
}

type terraformVariables struct {
	ProjectID           string   `json:"project_id"`
	Region              string   `json:"region"`
	Zone                string   `json:"zone"`
	DeploymentSource    string   `json:"deployment_source"`
	DockerImage         string   `json:"docker_image"`
	GitHubRepo          string   `json:"github_repo"`
	ContainerPort       int      `json:"container_port"`
	AllowedSourceRanges []string `json:"allowed_source_ranges"`
	MachineType         string   `json:"machine_type"`
	DiskSizeGB          int      `json:"disk_size_gb"`
}

func writeTerraformVariables(dir string, cfg DeployConfig) (string, error) {
	values := terraformVariables{
		ProjectID:           cfg.ProjectID,
		Region:              regionFromZone(cfg.Zone),
		Zone:                cfg.Zone,
		DeploymentSource:    cfg.Source,
		DockerImage:         cfg.DockerImage,
		GitHubRepo:          cfg.GithubRepo,
		ContainerPort:       cfg.ContainerPort,
		AllowedSourceRanges: append([]string(nil), cfg.AllowedSourceRanges...),
		MachineType:         cfg.MachineType,
		DiskSizeGB:          cfg.DiskSizeGB,
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return "", fmt.Errorf("Terraform 변수 직렬화: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, terraformVariablesName)
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("Terraform 변수 파일 경로가 안전한 일반 파일이 아닙니다")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("Terraform 변수 파일 확인: %w", statErr)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("Terraform 변수 파일 쓰기: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("Terraform 변수 파일 권한 설정: %w", err)
	}
	return path, nil
}

func readTerraformVariables(path string) (terraformVariables, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return terraformVariables{}, fmt.Errorf("Terraform 변수 파일 확인: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return terraformVariables{}, fmt.Errorf("Terraform 변수 파일 경로가 안전한 일반 파일이 아닙니다")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return terraformVariables{}, fmt.Errorf("Terraform 변수 파일 권한은 0600이어야 합니다")
	}

	file, err := os.Open(path)
	if err != nil {
		return terraformVariables{}, fmt.Errorf("Terraform 변수 파일 열기: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var values terraformVariables
	if err := decoder.Decode(&values); err != nil {
		return terraformVariables{}, fmt.Errorf("Terraform 변수 파일 파싱: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return terraformVariables{}, fmt.Errorf("Terraform 변수 파일에는 JSON 객체 하나만 있어야 합니다")
	}
	return values, nil
}

// Command describes one external process without exposing shell evaluation.
type Command struct {
	Name  string
	Args  []string
	Dir   string
	Stdin io.Reader
}

// CommandResult captures bounded process output for classification and reporting.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner is the boundary around Terraform and gcloud processes.
type Runner interface {
	Run(context.Context, Command) CommandResult
}

type ExecRunner struct{}

func (r ExecRunner) Run(ctx context.Context, command Command) CommandResult {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.Stdin = command.Stdin

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result
	}
	result.ExitCode = -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result
}

type FailureKind string

const (
	FailureInvalidConfig        FailureKind = "invalid_config"
	FailureTerraformFmt         FailureKind = "terraform_fmt"
	FailureTerraformInit        FailureKind = "terraform_init"
	FailureTerraformValidate    FailureKind = "terraform_validate"
	FailureTerraformPlan        FailureKind = "terraform_plan"
	FailureTerraformOutput      FailureKind = "terraform_output"
	FailureConfirmationRequired FailureKind = "confirmation_required"
	FailureTerraformApply       FailureKind = "terraform_apply"
	FailureZoneCapacity         FailureKind = "zone_capacity"
	FailureVMStartup            FailureKind = "vm_startup"
	FailureSSH                  FailureKind = "ssh"
	FailureDockerPull           FailureKind = "docker_pull"
	FailureDockerBuild          FailureKind = "docker_build"
	FailureDockerRun            FailureKind = "docker_run"
	FailureContainerStopped     FailureKind = "container_stopped"
	FailureHealthCheck          FailureKind = "health_check"
	FailureUnsafeDestroy        FailureKind = "unsafe_destroy"
	FailureTerraformDestroy     FailureKind = "terraform_destroy"
)

// DeploymentError preserves a machine-readable failure category and safe diagnostics.
type DeploymentError struct {
	Kind        FailureKind
	Operation   string
	Diagnostics string
}

func runTerraformApply(ctx context.Context, runner Runner, dir, planPath string, approved bool) error {
	if !approved {
		return &DeploymentError{Kind: FailureConfirmationRequired, Operation: "terraform apply", Diagnostics: "사용자 확인이 필요합니다"}
	}
	result := runner.Run(ctx, Command{
		Name: "terraform",
		Args: []string{"apply", "-input=false", "-no-color", planPath},
		Dir:  dir,
	})
	if result.ExitCode != 0 {
		kind := classifyTerraformFailure(result.Stdout + "\n" + result.Stderr)
		return &DeploymentError{Kind: kind, Operation: "terraform apply", Diagnostics: sanitizeDiagnostics(result.Stdout + "\n" + result.Stderr)}
	}
	return nil
}

func runTerraformDestroyApply(ctx context.Context, runner Runner, dir, planPath string, approved bool) error {
	if !approved {
		return &DeploymentError{Kind: FailureConfirmationRequired, Operation: "terraform destroy", Diagnostics: "사용자 확인이 필요합니다"}
	}
	result := runner.Run(ctx, Command{
		Name: "terraform",
		Args: []string{"apply", "-input=false", "-no-color", planPath},
		Dir:  dir,
	})
	if result.ExitCode != 0 {
		return &DeploymentError{Kind: FailureTerraformDestroy, Operation: "terraform destroy", Diagnostics: sanitizeDiagnostics(result.Stdout + "\n" + result.Stderr)}
	}
	return nil
}

func classifyTerraformFailure(raw string) FailureKind {
	lower := strings.ToLower(raw)
	capacitySignals := []string{
		"does not have enough resources available",
		"resource pool exhausted",
		"zone_resource_pool_exhausted",
		"zone_resource_pool_exhausted_with_details",
		"stockout",
	}
	for _, signal := range capacitySignals {
		if strings.Contains(lower, signal) {
			return FailureZoneCapacity
		}
	}
	return FailureTerraformApply
}

func (e *DeploymentError) Error() string {
	if e.Diagnostics == "" {
		return fmt.Sprintf("%s 실패 (%s)", e.Operation, e.Kind)
	}
	return fmt.Sprintf("%s 실패 (%s): %s", e.Operation, e.Kind, e.Diagnostics)
}

func runTerraformPreflight(ctx context.Context, runner Runner, dir string) error {
	steps := []struct {
		args []string
		kind FailureKind
		op   string
	}{
		{args: []string{"fmt", "-check", "-diff", "main.tf"}, kind: FailureTerraformFmt, op: "terraform fmt"},
		{args: []string{"init", "-backend=false", "-lockfile=readonly", "-input=false"}, kind: FailureTerraformInit, op: "terraform init"},
		{args: []string{"validate", "-no-color"}, kind: FailureTerraformValidate, op: "terraform validate"},
	}
	for _, step := range steps {
		result := runner.Run(ctx, Command{Name: "terraform", Args: step.args, Dir: dir})
		if result.ExitCode != 0 {
			return &DeploymentError{Kind: step.kind, Operation: step.op, Diagnostics: sanitizeDiagnostics(result.Stdout + "\n" + result.Stderr)}
		}
	}
	return nil
}

func runTerraformPlan(ctx context.Context, runner Runner, dir, planName string, destroy bool, out io.Writer) (bool, error) {
	args := []string{
		"plan",
		"-input=false",
		"-no-color",
		"-detailed-exitcode",
		"-out=" + planName,
		"-var-file=" + terraformVariablesName,
	}
	operation := "terraform plan"
	if destroy {
		args = append(args, "-destroy")
		operation = "terraform destroy plan"
	}
	result := runner.Run(ctx, Command{Name: "terraform", Args: args, Dir: dir})
	writeRedactedCommandOutput(out, result)
	switch result.ExitCode {
	case 0:
		return false, nil
	case 2:
		return true, nil
	default:
		return false, &DeploymentError{
			Kind:        FailureTerraformPlan,
			Operation:   operation,
			Diagnostics: sanitizeDiagnostics(result.Stdout + "\n" + result.Stderr),
		}
	}
}

func sanitizeDiagnostics(raw string) string {
	clean := strings.TrimSpace(redactSecrets(raw))
	if len(clean) > maxDiagnosticBytes {
		clean = clean[len(clean)-maxDiagnosticBytes:]
	}
	return clean
}

func redactSecrets(raw string) string {
	clean := raw
	for _, secretPattern := range diagnosticSecretPatterns {
		clean = secretPattern.pattern.ReplaceAllString(clean, secretPattern.replacement)
	}
	return clean
}

func writeRedactedCommandOutput(out io.Writer, result CommandResult) {
	if out == nil {
		return
	}
	for _, raw := range []string{result.Stdout, result.Stderr} {
		if raw == "" {
			continue
		}
		clean := redactSecrets(raw)
		fmt.Fprint(out, clean)
		if !strings.HasSuffix(clean, "\n") {
			fmt.Fprintln(out)
		}
	}
}
