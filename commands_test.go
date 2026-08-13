package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDestroyRefusesWithoutState(t *testing.T) {
	runner := &recordingRunner{}
	err := destroyTerraform(context.Background(), bytes.NewBufferString("yes\n"), &bytes.Buffer{}, runner, t.TempDir(), downOptions{})

	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeDestroy {
		t.Fatalf("unexpected error: %#v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("ran Terraform without state: %#v", runner.commands)
	}
}

func TestDestroyRefusesSymlinkedStateWithoutRunningTerraform(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "copied-state")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "terraform.tfstate")); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	err := destroyTerraform(context.Background(), bytes.NewBufferString("yes\n"), &bytes.Buffer{}, runner, dir, downOptions{})

	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeDestroy {
		t.Fatalf("unexpected error: %#v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("ran Terraform with a symlinked state: %#v", runner.commands)
	}
}

func TestDestroyRefusesANonDefaultWorkspaceBeforeReadingState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{results: []CommandResult{{Stdout: "production\n"}}}

	err := destroyTerraform(context.Background(), bytes.NewBufferString("yes\n"), &bytes.Buffer{}, runner, dir, downOptions{})
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeDestroy {
		t.Fatalf("unexpected error: %#v", err)
	}
	if len(runner.commands) != 1 || runner.commands[0].Args[0] != "workspace" {
		t.Fatalf("destroy commands = %#v, want only workspace check", runner.commands)
	}
}

func TestConfirmRequiresFullYes(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "\n", "no\n"} {
		approved, err := confirm(bytes.NewBufferString(answer), &bytes.Buffer{}, "confirm: ")
		if err != nil {
			t.Fatal(err)
		}
		if approved {
			t.Fatalf("confirm() approved %q", answer)
		}
	}
	approved, err := confirm(bytes.NewBufferString("YES\n"), &bytes.Buffer{}, "confirm: ")
	if err != nil || !approved {
		t.Fatalf("confirm() = %v, %v, want true, nil", approved, err)
	}
}

func TestValidateOwnedStateRejectsUnrelatedResources(t *testing.T) {
	if err := validateOwnedState("google_compute_instance.demo\ngoogle_storage_bucket.unrelated\n"); err == nil {
		t.Fatal("validateOwnedState() accepted an unrelated resource")
	}
}

func TestValidateOwnedStateAcceptsOnlyCurrentResources(t *testing.T) {
	state := "google_compute_network.demo\n" +
		"google_compute_subnetwork.demo\n" +
		"google_compute_firewall.http\n" +
		"google_compute_firewall.iap_ssh\n" +
		"google_compute_instance.demo\n"
	if err := validateOwnedState(state); err != nil {
		t.Fatalf("validateOwnedState() rejected current resources: %v", err)
	}

	legacy := "google_project_service.compute_api\ngoogle_compute_firewall.allow_http\ngoogle_compute_instance.free_vm\n"
	if err := validateOwnedState(legacy); err == nil {
		t.Fatal("validateOwnedState() accepted legacy resources without an explicit migration")
	}
}

func TestUpStateGuardRejectsLegacyStateBeforePlanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{results: []CommandResult{{
		Stdout: "default\n",
	}, {
		Stdout: "google_compute_firewall.allow_http\ngoogle_compute_instance.free_vm\ngoogle_project_service.compute_api\n",
	}}}
	cfg := testDeployConfig()

	err := guardUpState(context.Background(), runner, dir, cfg)
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeState {
		t.Fatalf("unexpected error: %#v", err)
	}
	if len(runner.commands) != 2 || runner.commands[1].Args[0] != "state" {
		t.Fatalf("commands = %#v, want workspace check then terraform state list", runner.commands)
	}
}

func TestUpStateGuardAllowsAWorkdirWithoutState(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{{Stdout: "default\n"}}}
	if err := guardUpState(context.Background(), runner, t.TempDir(), testDeployConfig()); err != nil {
		t.Fatalf("guardUpState() rejected a clean workdir: %v", err)
	}
	if len(runner.commands) != 1 || runner.commands[0].Args[0] != "workspace" {
		t.Fatalf("guardUpState() did not verify the workspace: %#v", runner.commands)
	}
}

func TestUpStateGuardRejectsAnOrphanedStateBackup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate.backup"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{results: []CommandResult{{Stdout: "default\n"}}}

	err := guardUpState(context.Background(), runner, dir, testDeployConfig())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeState {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestUpStateGuardRejectsANonDefaultWorkspace(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{{Stdout: "production\n"}}}

	err := guardUpState(context.Background(), runner, t.TempDir(), testDeployConfig())
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeState {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestUpStateGuardRejectsAProjectMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := testDeployConfig()
	if _, err := writeTerraformVariables(dir, existing); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: "default\n"},
		{Stdout: "google_compute_network.demo\ngoogle_compute_subnetwork.demo\ngoogle_compute_firewall.http\ngoogle_compute_firewall.iap_ssh\ngoogle_compute_instance.demo\n"},
		{Stdout: testTerraformOutputsJSON()},
	}}
	desired := existing
	desired.ProjectID = "another-project-123"

	err := guardUpState(context.Background(), runner, dir, desired)
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeState {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func testDeployConfig() DeployConfig {
	return DeployConfig{
		ProjectID:           "demo-project-123",
		Zone:                "us-central1-a",
		Source:              "docker",
		DockerImage:         "nginx:1.27.4",
		ContainerPort:       80,
		AllowedSourceRanges: []string{"203.0.113.10/32"},
		MachineType:         "e2-micro",
		DiskSizeGB:          10,
	}
}

func testTerraformOutputsJSON() string {
	return `{
		"project_id":{"sensitive":false,"value":"demo-project-123"},
		"region":{"sensitive":false,"value":"us-central1"},
		"vm_name":{"sensitive":false,"value":"gcp-free-deploy-demo"},
		"vm_zone":{"sensitive":false,"value":"us-central1-a"},
		"website_url":{"sensitive":false,"value":"http://203.0.113.10"},
		"generated_resources":{"sensitive":false,"value":["gcp-free-deploy-network"]}
	}`
}

func TestValidateDestroyTargetRejectsStateAndVariablesMismatch(t *testing.T) {
	outputs := testTerraformOutputs()
	variables := terraformVariables{
		ProjectID: "another-project-123",
		Region:    "us-central1",
		Zone:      "us-central1-a",
	}

	if err := validateDestroyTarget(outputs, variables); err == nil {
		t.Fatal("validateDestroyTarget() accepted mismatched project IDs")
	}

	variables.ProjectID = outputs.ProjectID.Value
	variables.Zone = "us-central1-b"
	if err := validateDestroyTarget(outputs, variables); err == nil {
		t.Fatal("validateDestroyTarget() accepted mismatched zones")
	}
}

func TestValidateDestroyTargetAcceptsMatchingDeployment(t *testing.T) {
	outputs := testTerraformOutputs()
	variables := terraformVariables{
		ProjectID: outputs.ProjectID.Value,
		Region:    outputs.Region.Value,
		Zone:      outputs.VMZone.Value,
	}

	if err := validateDestroyTarget(outputs, variables); err != nil {
		t.Fatalf("validateDestroyTarget() rejected a matching deployment: %v", err)
	}
}
