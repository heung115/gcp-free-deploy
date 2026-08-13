package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	defaultStartupTimeout = 15 * time.Minute
	minStartupTimeout     = 1 * time.Minute
	maxStartupTimeout     = 1 * time.Hour
	diagnosticTimeout     = 45 * time.Second
)

type TerraformValue[T any] struct {
	Sensitive bool `json:"sensitive"`
	Value     T    `json:"value"`
}

type TerraformOutputs struct {
	ProjectID          TerraformValue[string]   `json:"project_id"`
	Region             TerraformValue[string]   `json:"region"`
	VMName             TerraformValue[string]   `json:"vm_name"`
	VMZone             TerraformValue[string]   `json:"vm_zone"`
	WebsiteURL         TerraformValue[string]   `json:"website_url"`
	GeneratedResources TerraformValue[[]string] `json:"generated_resources"`
}

func parseTerraformOutputs(data []byte) (TerraformOutputs, error) {
	var outputs TerraformOutputs
	if err := json.Unmarshal(data, &outputs); err != nil {
		return TerraformOutputs{}, &DeploymentError{Kind: FailureTerraformOutput, Operation: "terraform output", Diagnostics: "JSON 출력 형식이 올바르지 않습니다"}
	}
	if outputs.ProjectID.Value == "" || outputs.Region.Value == "" || outputs.VMName.Value == "" || outputs.VMZone.Value == "" || outputs.WebsiteURL.Value == "" {
		return TerraformOutputs{}, &DeploymentError{Kind: FailureTerraformOutput, Operation: "terraform output", Diagnostics: "필수 output이 비어 있습니다"}
	}
	return outputs, nil
}

func readTerraformOutputs(ctx context.Context, runner Runner, dir string) (TerraformOutputs, error) {
	result := runner.Run(ctx, Command{Name: "terraform", Args: []string{"output", "-json", "-no-color"}, Dir: dir})
	if result.ExitCode != 0 {
		return TerraformOutputs{}, &DeploymentError{Kind: FailureTerraformOutput, Operation: "terraform output", Diagnostics: sanitizeDiagnostics(result.Stdout + "\n" + result.Stderr)}
	}
	return parseTerraformOutputs([]byte(result.Stdout))
}

type DeploymentMonitor struct {
	runner         Runner
	out            io.Writer
	startupTimeout time.Duration
	pollInterval   time.Duration
	healthChecks   int
}

func NewDeploymentMonitor(runner Runner, out io.Writer) *DeploymentMonitor {
	return &DeploymentMonitor{
		runner:         runner,
		out:            out,
		startupTimeout: defaultStartupTimeout,
		pollInterval:   4 * time.Second,
		healthChecks:   12,
	}
}

func (m *DeploymentMonitor) Wait(ctx context.Context, outputs TerraformOutputs) error {
	monitorCtx, cancel := context.WithTimeout(ctx, m.startupTimeout)
	defer cancel()

	startedAt := time.Now()
	sshConnected := false
	for poll := 1; ; poll++ {
		result := m.runRemote(monitorCtx, outputs, statusCommand())
		combined := result.Stdout + "\n" + result.Stderr
		if result.ExitCode == 0 {
			sshConnected = true
			if runtimeReady(combined) {
				return m.waitForPublicHealth(ctx, outputs.WebsiteURL.Value)
			}
			if failure := classifyRuntimeFailure(combined); failure != "" {
				return &DeploymentError{Kind: failure, Operation: "VM startup 검증", Diagnostics: m.collectDiagnosticsWithTimeout(ctx, outputs)}
			}
		}

		if monitorCtx.Err() != nil {
			break
		}
		elapsed := time.Since(startedAt).Round(time.Second)
		fmt.Fprintf(m.out, "startup 상태 대기 중 (확인 %d회, 경과 %s / 제한 %s)\n", poll, elapsed, m.startupTimeout)
		if err := waitContext(monitorCtx, m.pollInterval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
			return err
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 제한 시각과 startup 완료가 거의 동시에 발생할 수 있으므로 별도 짧은
	// context에서 상태를 마지막으로 한 번 확인한 뒤 실패로 판정한다.
	finalCtx, finalCancel := context.WithTimeout(ctx, diagnosticTimeout)
	finalResult := m.runRemote(finalCtx, outputs, statusCommand())
	finalCancel()
	finalCombined := finalResult.Stdout + "\n" + finalResult.Stderr
	if finalResult.ExitCode == 0 {
		sshConnected = true
		if runtimeReady(finalCombined) {
			return m.waitForPublicHealth(ctx, outputs.WebsiteURL.Value)
		}
		if failure := classifyRuntimeFailure(finalCombined); failure != "" {
			return &DeploymentError{Kind: failure, Operation: "VM startup 검증", Diagnostics: m.collectDiagnosticsWithTimeout(ctx, outputs)}
		}
	}

	kind := FailureVMStartup
	operation := "VM startup 검증"
	if !sshConnected {
		kind = FailureSSH
		operation = "IAP SSH 연결"
	}
	diagnostics := m.collectDiagnosticsWithTimeout(ctx, outputs)
	timeoutMessage := fmt.Sprintf("startup 제한 %s를 초과했습니다", m.startupTimeout)
	if diagnostics != "" {
		timeoutMessage += "\n" + diagnostics
	}
	return &DeploymentError{Kind: kind, Operation: operation, Diagnostics: timeoutMessage}
}

func runtimeReady(raw string) bool {
	return strings.Contains(raw, "STARTUP_DONE") &&
		strings.Contains(raw, "CONTAINER_RUNNING") &&
		strings.Contains(raw, "HTTP_HEALTH_OK")
}

func (m *DeploymentMonitor) runRemote(ctx context.Context, outputs TerraformOutputs, remoteCommand string) CommandResult {
	return m.runner.Run(ctx, Command{
		Name: "gcloud",
		Args: []string{
			"compute", "ssh", outputs.VMName.Value,
			"--project", outputs.ProjectID.Value,
			"--zone", outputs.VMZone.Value,
			"--tunnel-through-iap", "--quiet",
			"--command", remoteCommand,
			"--", "-o", "ConnectTimeout=10",
		},
	})
}

func statusCommand() string {
	return "sudo journalctl -t gcp-free-deploy -n 80 --no-pager; " +
		"if sudo docker inspect --format='CONTAINER_{{if .State.Running}}RUNNING{{else}}STOPPED{{end}}' web 2>/dev/null; then true; else echo CONTAINER_MISSING; fi; " +
		"if curl --fail --silent --show-error --connect-timeout 2 --max-time 5 http://127.0.0.1:80/ >/dev/null; then echo HTTP_HEALTH_OK; else echo HTTP_HEALTH_FAILED; fi"
}

func diagnosticsCommand() string {
	return "echo '[startup service]'; sudo systemctl status google-startup-scripts.service --no-pager --lines=40 || true; " +
		"echo '[startup log]'; sudo journalctl -t gcp-free-deploy -n 160 --no-pager || true; " +
		"echo '[containers]'; sudo docker ps -a --no-trunc || true; " +
		"echo '[container log]'; sudo docker logs --tail 120 web 2>&1 || true; " +
		"echo '[health]'; curl --fail --silent --show-error --connect-timeout 2 --max-time 5 http://127.0.0.1:80/ >/dev/null && echo HTTP_HEALTH_OK || echo HTTP_HEALTH_FAILED"
}

func (m *DeploymentMonitor) collectDiagnostics(ctx context.Context, outputs TerraformOutputs) string {
	result := m.runRemote(ctx, outputs, diagnosticsCommand())
	if result.ExitCode != 0 && strings.TrimSpace(result.Stdout) == "" {
		return sanitizeDiagnostics(result.Stderr)
	}
	return sanitizeDiagnostics(result.Stdout + "\n" + result.Stderr)
}

func (m *DeploymentMonitor) collectDiagnosticsWithTimeout(ctx context.Context, outputs TerraformOutputs) string {
	diagnosticCtx, cancel := context.WithTimeout(ctx, diagnosticTimeout)
	defer cancel()
	return m.collectDiagnostics(diagnosticCtx, outputs)
}

func (m *DeploymentMonitor) waitForPublicHealth(ctx context.Context, websiteURL string) error {
	for attempt := 1; attempt <= m.healthChecks; attempt++ {
		result := m.runner.Run(ctx, Command{Name: "curl", Args: []string{"--fail", "--silent", "--show-error", "--connect-timeout", "3", "--max-time", "10", websiteURL}})
		if result.ExitCode == 0 {
			return nil
		}
		if attempt < m.healthChecks {
			if err := waitContext(ctx, m.pollInterval); err != nil {
				return err
			}
		}
	}
	return &DeploymentError{Kind: FailureHealthCheck, Operation: "외부 HTTP health check", Diagnostics: "VM 내부 검증은 통과했지만 외부 HTTP 요청이 실패했습니다"}
}

func classifyRuntimeFailure(raw string) FailureKind {
	switch {
	case strings.Contains(raw, "FAILURE_DOCKER_PULL"):
		return FailureDockerPull
	case strings.Contains(raw, "FAILURE_DOCKER_BUILD"):
		return FailureDockerBuild
	case strings.Contains(raw, "FAILURE_DOCKER_RUN"):
		return FailureDockerRun
	case strings.Contains(raw, "CONTAINER_NOT_RUNNING"), strings.Contains(raw, "CONTAINER_STOPPED"):
		return FailureContainerStopped
	case strings.Contains(raw, "FAILURE_HTTP_HEALTH"):
		return FailureHealthCheck
	case strings.Contains(raw, "STARTUP_DONE") && strings.Contains(raw, "CONTAINER_MISSING"):
		return FailureContainerStopped
	case strings.Contains(raw, "STARTUP_DONE") && strings.Contains(raw, "HTTP_HEALTH_FAILED"):
		return FailureHealthCheck
	case strings.Contains(raw, "STARTUP_FAILED"):
		return FailureVMStartup
	default:
		return ""
	}
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
