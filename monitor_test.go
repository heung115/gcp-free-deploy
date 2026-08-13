package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
		!strings.Contains(deployErr.Diagnostics, "startup 제한") {
		t.Fatalf("timeout diagnostics = %q", deployErr.Diagnostics)
	}
}
