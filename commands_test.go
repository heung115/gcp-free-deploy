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
