package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type deadlineRecordingRunner struct {
	calls                   int
	publicHealthHasDeadline bool
}

func (r *deadlineRecordingRunner) LookPath(string) error { return nil }

func (r *deadlineRecordingRunner) Run(ctx context.Context, command Command) CommandResult {
	r.calls++
	if r.calls == 1 {
		return CommandResult{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"}
	}
	if command.Name == "curl" {
		_, r.publicHealthHasDeadline = ctx.Deadline()
		return CommandResult{ExitCode: 22, Stderr: "HTTP 503"}
	}
	return CommandResult{}
}

type deadlineBoundaryRunner struct {
	calls               int
	finalHealthSucceeds bool
}

type statusDeadlineRunner struct {
	calls int
}

func (r *statusDeadlineRunner) LookPath(string) error { return nil }

func (r *statusDeadlineRunner) Run(ctx context.Context, _ Command) CommandResult {
	r.calls++
	if r.calls <= 2 {
		<-ctx.Done()
		return CommandResult{ExitCode: 1, Stderr: ctx.Err().Error()}
	}
	return CommandResult{Stdout: "startup status unavailable"}
}

func (r *deadlineBoundaryRunner) LookPath(string) error { return nil }

func (r *deadlineBoundaryRunner) Run(ctx context.Context, command Command) CommandResult {
	r.calls++
	switch r.calls {
	case 1, 3:
		return CommandResult{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"}
	case 2:
		<-ctx.Done()
		return CommandResult{ExitCode: 28, Stderr: ctx.Err().Error()}
	case 4:
		if r.finalHealthSucceeds {
			return CommandResult{}
		}
		<-ctx.Done()
		return CommandResult{ExitCode: 28, Stderr: ctx.Err().Error()}
	default:
		return CommandResult{ExitCode: 1, Stderr: "unexpected command"}
	}
}

func testTerraformOutputs() TerraformOutputs {
	return TerraformOutputs{
		ProjectID:  TerraformValue[string]{Value: "demo-project-123"},
		Region:     TerraformValue[string]{Value: "us-central1"},
		VMName:     TerraformValue[string]{Value: "gcp-free-deploy-demo"},
		VMZone:     TerraformValue[string]{Value: "us-central1-a"},
		WebsiteURL: TerraformValue[string]{Value: "http://203.0.113.10"},
	}
}

func TestParseTerraformOutputs(t *testing.T) {
	data := []byte(`{
		"project_id":{"sensitive":false,"value":"demo-project-123"},
		"region":{"sensitive":false,"value":"us-central1"},
		"vm_name":{"sensitive":false,"value":"gcp-free-deploy-demo"},
		"vm_zone":{"sensitive":false,"value":"us-central1-a"},
		"website_url":{"sensitive":false,"value":"http://203.0.113.10"},
		"generated_resources":{"sensitive":false,"value":["gcp-free-deploy-network"]}
	}`)

	outputs, err := parseTerraformOutputs(data)
	if err != nil {
		t.Fatalf("parseTerraformOutputs() returned an error: %v", err)
	}
	if outputs.ProjectID.Value != "demo-project-123" || outputs.VMZone.Value != "us-central1-a" {
		t.Fatalf("unexpected outputs: %#v", outputs)
	}
}

func TestMonitorClassifiesStartupFailureAndCollectsDiagnostics(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "STARTUP_FAILED step=docker_build"},
		{Stdout: "FAILURE_DOCKER_BUILD\ncontainer log"},
	}}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.pollInterval = 0

	err := monitor.Wait(context.Background(), testTerraformOutputs())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureVMStartup {
		t.Fatalf("unexpected error: %#v", err)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("ran %d commands, want status and diagnostics", len(runner.commands))
	}
}

func TestMonitorReportsExternalHealthFailure(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"},
		{ExitCode: 22, Stderr: "HTTP 503"},
	}}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.healthChecks = 1
	monitor.pollInterval = time.Nanosecond

	err := monitor.Wait(context.Background(), testTerraformOutputs())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureHealthCheck {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestMonitorKeepsPublicHealthChecksInsideTheStartupDeadline(t *testing.T) {
	runner := &deadlineRecordingRunner{}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.healthChecks = 1

	err := monitor.Wait(context.Background(), testTerraformOutputs())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureHealthCheck {
		t.Fatalf("unexpected error: %#v", err)
	}
	if !runner.publicHealthHasDeadline {
		t.Fatal("external HTTP health check escaped the startup deadline")
	}
}

func TestMonitorUsesFinalGraceAfterMainHealthCheckConsumesStartupDeadline(t *testing.T) {
	runner := &deadlineBoundaryRunner{finalHealthSucceeds: true}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.startupTimeout = time.Millisecond
	monitor.finalCheckTimeout = 50 * time.Millisecond
	monitor.healthChecks = 1

	if err := monitor.Wait(context.Background(), testTerraformOutputs()); err != nil {
		t.Fatalf("Wait() did not use the final health-check grace period: %v", err)
	}
	if runner.calls != 4 {
		t.Fatalf("runner calls = %d, want initial status/health and final status/health", runner.calls)
	}
}

func TestMonitorReportsTypedHealthFailureWhenFinalGraceExpires(t *testing.T) {
	runner := &deadlineBoundaryRunner{}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.startupTimeout = time.Millisecond
	monitor.finalCheckTimeout = time.Millisecond
	monitor.healthChecks = 1

	err := monitor.Wait(context.Background(), testTerraformOutputs())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureHealthCheck {
		t.Fatalf("unexpected error: %#v", err)
	}
	if deployErr.Operation != "external HTTP health check" {
		t.Fatalf("operation = %q, want external HTTP health check", deployErr.Operation)
	}
}

func TestMonitorClassifiesFinalStatusProbeDeadlineAndCollectsDiagnostics(t *testing.T) {
	runner := &statusDeadlineRunner{}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.startupTimeout = time.Millisecond
	monitor.finalCheckTimeout = time.Millisecond

	err := monitor.Wait(context.Background(), testTerraformOutputs())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureSSH {
		t.Fatalf("Wait() error = %#v, want typed SSH failure", err)
	}
	if !strings.Contains(deployErr.Diagnostics, "startup status unavailable") {
		t.Fatalf("diagnostics = %q, want final diagnostic output", deployErr.Diagnostics)
	}
	if runner.calls != 3 {
		t.Fatalf("runner calls = %d, want initial status, final status, and diagnostics", runner.calls)
	}
}

func TestMonitorPropagatesCancellationDuringPublicHealthCheck(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"},
		{ExitCode: 28, Stderr: "canceled"},
	}}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.healthChecks = 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := monitor.Wait(ctx, testTerraformOutputs()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context cancellation", err)
	}
}

func TestMonitorQueriesOnlyCurrentBootJournal(t *testing.T) {
	for name, command := range map[string]string{
		"status":      statusCommand(),
		"diagnostics": diagnosticsCommand(),
	} {
		if !strings.Contains(command, "journalctl -b -t gcp-free-deploy") {
			t.Fatalf("%s command does not limit startup logs to the current boot: %q", name, command)
		}
	}
}

func TestMonitorWaitsWhileContainerIsNotCreatedYet(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "STARTUP_BEGIN\nCONTAINER_MISSING\nHTTP_HEALTH_FAILED"},
		{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"},
		{},
	}}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.healthChecks = 1
	monitor.pollInterval = time.Nanosecond

	if err := monitor.Wait(context.Background(), testTerraformOutputs()); err != nil {
		t.Fatalf("Wait() treated an in-progress startup as failed: %v", err)
	}
}

func TestMonitorClassifiesContainerMissingAfterStartupCompleted(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "STARTUP_DONE\nCONTAINER_MISSING\nHTTP_HEALTH_FAILED"},
		{Stdout: "STARTUP_DONE\nCONTAINER_MISSING\nHTTP_HEALTH_FAILED"},
	}}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.pollInterval = 0

	err := monitor.Wait(context.Background(), testTerraformOutputs())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureContainerStopped {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestMonitorClassifiesInternalHealthFailureAfterStartupCompleted(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_FAILED"},
		{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_FAILED"},
	}}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.pollInterval = 0

	err := monitor.Wait(context.Background(), testTerraformOutputs())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureHealthCheck {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestMonitorPerformsAFinalSuccessCheckAtTheTimeoutBoundary(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "STARTUP_BEGIN\nCONTAINER_MISSING\nHTTP_HEALTH_FAILED"},
		{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"},
		{},
	}}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.startupTimeout = time.Millisecond
	monitor.pollInterval = 2 * time.Millisecond
	monitor.healthChecks = 1

	if err := monitor.Wait(context.Background(), testTerraformOutputs()); err != nil {
		t.Fatalf("Wait() missed readiness at the timeout boundary: %v", err)
	}
}

func TestMonitorTimeoutIncludesFinalDiagnostics(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "STARTUP_BEGIN\nCONTAINER_MISSING\nHTTP_HEALTH_FAILED"},
		{Stdout: "STARTUP_BEGIN\nCONTAINER_MISSING\nHTTP_HEALTH_FAILED"},
		{Stdout: "startup is still installing packages"},
	}}
	monitor := NewDeploymentMonitor(runner, &bytes.Buffer{})
	monitor.startupTimeout = time.Millisecond
	monitor.pollInterval = 2 * time.Millisecond

	err := monitor.Wait(context.Background(), testTerraformOutputs())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureVMStartup {
		t.Fatalf("unexpected error: %#v", err)
	}
	if !strings.Contains(deployErr.Diagnostics, "startup is still installing packages") ||
		!strings.Contains(deployErr.Diagnostics, "startup timed out") {
		t.Fatalf("timeout diagnostics = %q", deployErr.Diagnostics)
	}
}
