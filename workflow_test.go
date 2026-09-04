package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type recordingRunner struct {
	commands []Command
	results  []CommandResult
	missing  map[string]bool
}

func (r *recordingRunner) LookPath(name string) error {
	if r.missing[name] {
		return errors.New("executable not found")
	}
	return nil
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
		{Name: "terraform", Args: []string{"init", "-reconfigure", "-lockfile=readonly", "-input=false"}, Dir: "/work"},
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

func TestBoundedTailBufferRetainsOnlyTheNewestOutput(t *testing.T) {
	var buffer boundedTailBuffer
	prefix := strings.Repeat("a", maxCommandOutputBytes+17)
	if written, err := buffer.Write([]byte(prefix)); err != nil || written != len(prefix) {
		t.Fatalf("Write() = %d, %v; want %d, nil", written, err, len(prefix))
	}
	if written, err := buffer.Write([]byte("newest-output")); err != nil || written != len("newest-output") {
		t.Fatalf("second Write() = %d, %v", written, err)
	}
	got := buffer.String()
	if !strings.Contains(got, "[output truncated") {
		t.Fatalf("bounded output omitted truncation marker: %q", got[:min(len(got), 80)])
	}
	if !strings.HasSuffix(got, "newest-output") {
		t.Fatal("bounded output did not retain the newest bytes")
	}
	if len(buffer.data) != maxCommandOutputBytes {
		t.Fatalf("retained %d bytes, want %d", len(buffer.data), maxCommandOutputBytes)
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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
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
	roundTrip, err := readTerraformVariables(path)
	if err != nil {
		t.Fatalf("readTerraformVariables() returned an error after writing: %v", err)
	}
	if roundTrip.ProjectID != cfg.ProjectID || roundTrip.Zone != cfg.Zone {
		t.Fatalf("round-trip variables = %#v, want project %q and zone %q", roundTrip, cfg.ProjectID, cfg.Zone)
	}
}

func TestTerraformVariablesModePassesValidationForOS(t *testing.T) {
	tests := []struct {
		name string
		goos string
		mode os.FileMode
		want bool
	}{
		{name: "Windows ignores POSIX mode bits", goos: "windows", mode: 0o666, want: true},
		{name: "private POSIX file", goos: "linux", mode: 0o600, want: true},
		{name: "read-only private POSIX file", goos: "darwin", mode: 0o400, want: true},
		{name: "group-readable POSIX file", goos: "linux", mode: 0o640, want: false},
		{name: "world-readable POSIX file", goos: "darwin", mode: 0o604, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := terraformVariablesModePassesValidationForOS(tt.goos, tt.mode); got != tt.want {
				t.Fatalf("terraformVariablesModePassesValidationForOS(%q, %04o) = %v, want %v", tt.goos, tt.mode, got, tt.want)
			}
		})
	}
}

func TestAtomicWriteRenameFailureDoesNotTruncateExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, terraformVariablesName)
	original := []byte("original deployment variables\n")
	replacement := []byte("incomplete replacement")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	renameErr := errors.New("injected rename failure")
	err := writeFileSafelyWithRename(path, replacement, 0o600, func(oldPath, newPath string) error {
		if filepath.Dir(oldPath) != dir {
			t.Errorf("temporary file directory = %q, want %q", filepath.Dir(oldPath), dir)
		}
		if newPath != path {
			t.Errorf("rename destination = %q, want %q", newPath, path)
		}
		temporaryData, readErr := os.ReadFile(oldPath)
		if readErr != nil {
			t.Errorf("read temporary file before rename: %v", readErr)
		} else if !bytes.Equal(temporaryData, replacement) {
			t.Errorf("temporary file content = %q, want %q", temporaryData, replacement)
		}
		return renameErr
	})
	if !errors.Is(err, renameErr) {
		t.Fatalf("writeFileSafelyWithRename() error = %v, want wrapped %v", err, renameErr)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("existing file was changed after failed atomic write: got %q, want %q", got, original)
	}

	temporaryFiles, err := filepath.Glob(filepath.Join(dir, filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("temporary files remain after failed atomic write: %v", temporaryFiles)
	}
}

func TestAtomicWriteReplacesExistingFileCompletely(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, terraformVariablesName)
	if err := os.WriteFile(path, []byte("old content with a long trailing suffix\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := []byte("new\n")
	if err := writeFileSafely(path, want, 0o600); err != nil {
		t.Fatalf("writeFileSafely() returned an error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("destination content = %q, want %q", got, want)
	}

	temporaryFiles, err := filepath.Glob(filepath.Join(dir, filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("temporary files remain after successful atomic write: %v", temporaryFiles)
	}
}
