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
		return TerraformOutputs{}, &DeploymentError{Kind: FailureTerraformOutput, Operation: "terraform output", Diagnostics: "invalid JSON output format"}
	}
	if outputs.ProjectID.Value == "" || outputs.Region.Value == "" || outputs.VMName.Value == "" || outputs.VMZone.Value == "" || outputs.WebsiteURL.Value == "" {
		return TerraformOutputs{}, &DeploymentError{Kind: FailureTerraformOutput, Operation: "terraform output", Diagnostics: "required output values are empty"}
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
	runner            Runner
	out               io.Writer
	startupTimeout    time.Duration
	finalCheckTimeout time.Duration
	pollInterval      time.Duration
	healthChecks      int
}

func NewDeploymentMonitor(runner Runner, out io.Writer) *DeploymentMonitor {
	return &DeploymentMonitor{
		runner:            runner,
		out:               out,
		startupTimeout:    defaultStartupTimeout,
		finalCheckTimeout: diagnosticTimeout,
		pollInterval:      4 * time.Second,
		healthChecks:      12,
	}
}

func (m *DeploymentMonitor) Wait(ctx context.Context, outputs TerraformOutputs) error {
	monitorCtx, cancel := context.WithTimeout(ctx, m.startupTimeout)
	defer cancel()

	startedAt := time.Now()
	sshConnected := false
	initialHealthTimedOut := false
	for poll := 1; ; poll++ {
		result := m.runRemote(monitorCtx, outputs, statusCommand())
		combined := result.Stdout + "\n" + result.Stderr
		if result.ExitCode == 0 {
			sshConnected = true
			if runtimeReady(combined) {
				err := m.waitForPublicHealth(monitorCtx, outputs.WebsiteURL.Value)
				if err == nil {
					return nil
				}
				if errors.Is(err, context.DeadlineExceeded) {
					initialHealthTimedOut = true
					break
				}
				return err
			}
			if failure := classifyRuntimeFailure(combined); failure != "" {
				return &DeploymentError{Kind: failure, Operation: "VM startup verification", Diagnostics: m.collectDiagnosticsWithTimeout(ctx, outputs)}
			}
		}

		if monitorCtx.Err() != nil {
			break
		}
		elapsed := time.Since(startedAt).Round(time.Second)
		fmt.Fprintf(m.out, "Waiting for startup status (check %d, elapsed %s / timeout %s)\n", poll, elapsed, m.startupTimeout)
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

	// The timeout and startup completion can occur almost simultaneously, so check
	// the status one final time in a separate short-lived context before failing.
	finalCtx, finalCancel := context.WithTimeout(ctx, m.finalCheckTimeout)
	finalResult := m.runRemote(finalCtx, outputs, statusCommand())
	defer finalCancel()
	finalCombined := finalResult.Stdout + "\n" + finalResult.Stderr
	if finalResult.ExitCode == 0 {
		sshConnected = true
		if runtimeReady(finalCombined) {
			err := m.waitForPublicHealth(finalCtx, outputs.WebsiteURL.Value)
			if err == nil {
				return nil
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return finalHealthTimeoutError()
			}
			return err
		}
		if failure := classifyRuntimeFailure(finalCombined); failure != "" {
			return &DeploymentError{Kind: failure, Operation: "VM startup verification", Diagnostics: m.collectDiagnosticsWithTimeout(ctx, outputs)}
		}
	}
	if err := finalCtx.Err(); err != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return parentErr
		}
		if initialHealthTimedOut && errors.Is(err, context.DeadlineExceeded) {
			return finalHealthTimeoutError()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}

	kind := FailureVMStartup
	operation := "VM startup verification"
	if !sshConnected {
		kind = FailureSSH
		operation = "IAP SSH connection"
	}
	diagnostics := m.collectDiagnosticsWithTimeout(ctx, outputs)
	timeoutMessage := fmt.Sprintf("startup timed out after %s", m.startupTimeout)
	if diagnostics != "" {
		timeoutMessage += "\n" + diagnostics
	}
	return &DeploymentError{Kind: kind, Operation: operation, Diagnostics: timeoutMessage}
}

func finalHealthTimeoutError() error {
	return &DeploymentError{
		Kind:        FailureHealthCheck,
		Operation:   "external HTTP health check",
		Diagnostics: "the startup deadline and final external health-check grace period both expired",
	}
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
	return "sudo journalctl -b -t gcp-free-deploy -n 80 --no-pager; " +
		"if sudo docker inspect --format='CONTAINER_{{if .State.Running}}RUNNING{{else}}STOPPED{{end}}' web 2>/dev/null; then true; else echo CONTAINER_MISSING; fi; " +
		"if curl --fail --silent --show-error --connect-timeout 2 --max-time 5 http://127.0.0.1:80/ >/dev/null; then echo HTTP_HEALTH_OK; else echo HTTP_HEALTH_FAILED; fi"
}

func diagnosticsCommand() string {
	return "echo '[startup service]'; sudo systemctl status google-startup-scripts.service --no-pager --lines=40 || true; " +
		"echo '[startup log]'; sudo journalctl -b -t gcp-free-deploy -n 160 --no-pager || true; " +
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt < m.healthChecks {
			if err := waitContext(ctx, m.pollInterval); err != nil {
				return err
			}
		}
	}
	return &DeploymentError{Kind: FailureHealthCheck, Operation: "external HTTP health check", Diagnostics: "the in-VM check passed, but the external HTTP request failed"}
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
