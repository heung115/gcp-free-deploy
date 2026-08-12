package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type recordingRunner struct {
	commands []Command
	results  []CommandResult
}

func (r *recordingRunner) Run(_ context.Context, command Command) CommandResult {
	r.commands = append(r.commands, command)
	if len(r.results) == 0 {
		return CommandResult{}
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result
}

func TestTerraformPreflightRunsSafetyChecksInOrder(t *testing.T) {
	runner := &recordingRunner{}

	if err := runTerraformPreflight(context.Background(), runner, "/work"); err != nil {
		t.Fatalf("runTerraformPreflight() returned an error: %v", err)
	}

	want := []Command{
		{Name: "terraform", Args: []string{"fmt", "-check", "-diff", "main.tf"}, Dir: "/work"},
		{Name: "terraform", Args: []string{"init", "-backend=false", "-lockfile=readonly", "-input=false"}, Dir: "/work"},
		{Name: "terraform", Args: []string{"validate", "-no-color"}, Dir: "/work"},
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands differ\n got: %#v\nwant: %#v", runner.commands, want)
	}
}

func TestTerraformPreflightStopsAtFirstFailure(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{},
		{ExitCode: 1, Stderr: "Error: provider initialization failed"},
	}}

	err := runTerraformPreflight(context.Background(), runner, "/work")
	if err == nil {
		t.Fatal("runTerraformPreflight() returned nil after init failed")
	}
	deployErr, ok := err.(*DeploymentError)
	if !ok || deployErr.Kind != FailureTerraformInit {
		t.Fatalf("unexpected error: %#v", err)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("ran %d commands after failure, want 2", len(runner.commands))
	}
}

func TestApplyRequiresApproval(t *testing.T) {
	runner := &recordingRunner{}

	err := runTerraformApply(context.Background(), runner, "/work", ".gcp-free-deploy.tfplan", false)
	if err == nil {
		t.Fatal("runTerraformApply() returned nil without approval")
	}
	deployErr, ok := err.(*DeploymentError)
	if !ok || deployErr.Kind != FailureConfirmationRequired {
		t.Fatalf("unexpected error: %#v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("ran commands without approval: %#v", runner.commands)
	}
}

func TestApprovedApplyUsesTheSavedPlan(t *testing.T) {
	runner := &recordingRunner{}

	if err := runTerraformApply(context.Background(), runner, "/work", ".gcp-free-deploy.tfplan", true); err != nil {
		t.Fatalf("runTerraformApply() returned an error: %v", err)
	}

	want := []Command{{
		Name: "terraform",
		Args: []string{"apply", "-input=false", "-no-color", ".gcp-free-deploy.tfplan"},
		Dir:  "/work",
	}}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands differ\n got: %#v\nwant: %#v", runner.commands, want)
	}
}

func TestDestroyRequiresApproval(t *testing.T) {
	runner := &recordingRunner{}

	err := runTerraformDestroyApply(context.Background(), runner, "/work", ".gcp-free-deploy-destroy.tfplan", false)
	if err == nil {
		t.Fatal("runTerraformDestroyApply() returned nil without approval")
	}
	deployErr, ok := err.(*DeploymentError)
	if !ok || deployErr.Kind != FailureConfirmationRequired {
		t.Fatalf("unexpected error: %#v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("ran commands without approval: %#v", runner.commands)
	}
}

func TestSanitizeDiagnosticsMasksSecretsAndLimitsOutput(t *testing.T) {
	raw := strings.Repeat("old diagnostic\n", 700) +
		"Authorization: Bearer example-bearer-secret\n" +
		`{"access_token":"token-value","client_secret":"client-secret-value"}` + "\n" +
		"PASSWORD=plain-password\n" +
		"-----BEGIN TEST PRIVATE KEY-----\nprivate-material\n-----END TEST PRIVATE KEY-----\n"

	clean := sanitizeDiagnostics(raw)
	for _, secret := range []string{"example-bearer-secret", "token-value", "client-secret-value", "plain-password", "private-material"} {
		if strings.Contains(clean, secret) {
			t.Fatalf("sanitized diagnostics still contain %q", secret)
		}
	}
	if !strings.Contains(clean, "[REDACTED]") {
		t.Fatal("sanitized diagnostics do not mark redacted values")
	}
	if len(clean) > maxDiagnosticBytes {
		t.Fatalf("sanitized diagnostics length = %d, want <= %d", len(clean), maxDiagnosticBytes)
	}
}

func TestTerraformPlanRedactsOutputBeforeDisplayingIt(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{{
		ExitCode: 2,
		Stdout:   "Plan: 1 to add\nMY_APP_SECRET=do-not-print\n",
		Stderr:   "Authorization: Basic dXNlcjpwYXNz\n",
	}}}
	var out bytes.Buffer

	hasChanges, err := runTerraformPlan(context.Background(), runner, "/work", ".gcp-free-deploy.tfplan", false, &out)
	if err != nil || !hasChanges {
		t.Fatalf("runTerraformPlan() = %v, %v, want true, nil", hasChanges, err)
	}
	displayed := out.String()
	for _, secret := range []string{"do-not-print", "dXNlcjpwYXNz"} {
		if strings.Contains(displayed, secret) {
			t.Fatalf("displayed plan still contains %q", secret)
		}
	}
	if !strings.Contains(displayed, "Plan: 1 to add") || !strings.Contains(displayed, "[REDACTED]") {
		t.Fatalf("displayed plan = %q", displayed)
	}
}

func TestClassifyTerraformFailureRecognizesZoneCapacity(t *testing.T) {
	if got := classifyTerraformFailure("Error 503: ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS"); got != FailureZoneCapacity {
		t.Fatalf("classifyTerraformFailure() = %q, want %q", got, FailureZoneCapacity)
	}
	if got := classifyTerraformFailure("Error 403: permission denied"); got != FailureTerraformApply {
		t.Fatalf("classifyTerraformFailure() = %q, want %q", got, FailureTerraformApply)
	}
}

func TestWriteTerraformVariablesUsesJSON(t *testing.T) {
	dir := t.TempDir()
	cfg := DeployConfig{
		ProjectID:           "demo-project-123",
		Zone:                "us-central1-a",
		Source:              "docker",
		DockerImage:         `ghcr.io/example/demo:1.0`,
		ContainerPort:       8080,
		AllowedSourceRanges: []string{"203.0.113.10/32"},
		MachineType:         "e2-micro",
		DiskSizeGB:          10,
	}

	path, err := writeTerraformVariables(dir, cfg)
	if err != nil {
		t.Fatalf("writeTerraformVariables() returned an error: %v", err)
	}
	if filepath.Base(path) != terraformVariablesName {
		t.Fatalf("path = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	var values map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatalf("generated variables are not JSON: %v", err)
	}
	if values["deployment_source"] != "docker" || values["region"] != "us-central1" {
		t.Fatalf("unexpected variables: %#v", values)
	}
}
