package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseUpOptionsUsesABoundedConfigurableStartupTimeout(t *testing.T) {
	opts, err := parseUpOptions(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseUpOptions() returned an error: %v", err)
	}
	if opts.StartupTimeout != defaultStartupTimeout {
		t.Fatalf("default startup timeout = %s, want %s", opts.StartupTimeout, defaultStartupTimeout)
	}

	opts, err = parseUpOptions([]string{"--startup-timeout", "20m"}, &bytes.Buffer{})
	if err != nil || opts.StartupTimeout != 20*time.Minute {
		t.Fatalf("configured startup timeout = %s, %v", opts.StartupTimeout, err)
	}

	for _, value := range []string{"30s", "2h"} {
		if _, err := parseUpOptions([]string{"--startup-timeout", value}, &bytes.Buffer{}); err == nil {
			t.Fatalf("parseUpOptions() accepted unsafe startup timeout %s", value)
		}
	}
}

func TestCommandHelpReturnsSuccessWithoutRunningAnything(t *testing.T) {
	tests := [][]string{
		{"--help"},
		{"init", "--help"},
		{"up", "--help"},
		{"down", "--help"},
		{"validate", "--help"},
	}

	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			runner := &recordingRunner{}
			var out bytes.Buffer
			var errOut bytes.Buffer
			if err := runCLI(context.Background(), args, &bytes.Buffer{}, &out, &errOut, runner, t.TempDir()); err != nil {
				t.Fatalf("runCLI(%q) returned an error: %v", args, err)
			}
			if out.Len() == 0 && errOut.Len() == 0 {
				t.Fatalf("runCLI(%q) printed no help", args)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("runCLI(%q) ran commands: %#v", args, runner.commands)
			}
		})
	}
}

func TestTopLevelHelpExplainsCommandsAndSharedWorkingDirectory(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)
	help := out.String()
	for _, want := range []string{
		"init      Prepare",
		"validate  Check",
		"up        Plan",
		"down      Delete",
		"version   Print",
		"same working directory",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("top-level help = %q, want it to contain %q", help, want)
		}
	}
}

func TestVersionCommandsUseBuildTimeVersion(t *testing.T) {
	previous := buildVersion
	buildVersion = "v1.2.3"
	t.Cleanup(func() { buildVersion = previous })

	for _, args := range [][]string{{"version"}, {"--version"}} {
		var out bytes.Buffer
		if err := runCLI(context.Background(), args, &bytes.Buffer{}, &out, &bytes.Buffer{}, &recordingRunner{}, t.TempDir()); err != nil {
			t.Fatalf("runCLI(%q) returned an error: %v", args, err)
		}
		if got, want := out.String(), "gcp-free-deploy v1.2.3\n"; got != want {
			t.Fatalf("runCLI(%q) output = %q, want %q", args, got, want)
		}
	}
}

func TestFreeTierAssessmentExplainsProfileAndExternalIPv4Cost(t *testing.T) {
	tests := []struct {
		name string
		cfg  DeployConfig
		want string
	}{
		{name: "matching us-central1 profile", cfg: DeployConfig{Zone: "us-central1-a", MachineType: "e2-micro"}, want: "matches e2-micro in eligible region us-central1"},
		{name: "matching us-east1 profile", cfg: DeployConfig{Zone: "us-east1-b", MachineType: "e2-micro"}, want: "matches e2-micro in eligible region us-east1"},
		{name: "matching us-west1 profile", cfg: DeployConfig{Zone: "us-west1-c", MachineType: "e2-micro"}, want: "matches e2-micro in eligible region us-west1"},
		{name: "ineligible region", cfg: DeployConfig{Zone: "asia-northeast3-a", MachineType: "e2-micro"}, want: "region asia-northeast3 is not an eligible"},
		{name: "ineligible machine", cfg: DeployConfig{Zone: "us-east1-b", MachineType: "e2-small"}, want: "machine type e2-small is not e2-micro"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printFreeTierAssessment(&out, tt.cfg)
			got := out.String()
			if !strings.Contains(got, tt.want) {
				t.Fatalf("assessment = %q, want it to contain %q", got, tt.want)
			}
			if !strings.Contains(got, "separately priced external IPv4 address") {
				t.Fatalf("assessment omitted external IPv4 cost warning: %q", got)
			}
		})
	}
}

func TestDeploymentSummaryIncludesFreeTierAssessment(t *testing.T) {
	cfg := testDeployConfig()
	var out bytes.Buffer
	printDeploymentSummary(&out, cfg, upOptions{StartupTimeout: defaultStartupTimeout})

	if !strings.Contains(out.String(), "Free Tier VM profile") || !strings.Contains(out.String(), "separately priced external IPv4 address") {
		t.Fatalf("deployment summary omitted Free Tier assessment: %q", out.String())
	}
}

func TestDeployRejectsAdditionalTerraformBeforeRunningTerraform(t *testing.T) {
	dir := t.TempDir()
	configPath := writeTestDeployConfig(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "extra.tf"), []byte("resource \"google_compute_disk\" \"unrelated\" {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}

	err := deployTerraform(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, runner, dir, upOptions{
		ConfigPath:     configPath,
		PlanOnly:       true,
		StartupTimeout: defaultStartupTimeout,
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected Terraform configuration file extra.tf") {
		t.Fatalf("deployTerraform() error = %v, want extra Terraform rejection", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("deploy ran Terraform before rejecting extra.tf: %#v", runner.commands)
	}
}

func TestDeployRejectsSymlinkedPlanBeforeRunningTerraform(t *testing.T) {
	dir := t.TempDir()
	configPath := writeTestDeployConfig(t, dir)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	createTestSymlink(t, target, filepath.Join(dir, applyPlanName))
	runner := &recordingRunner{}

	err := deployTerraform(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, runner, dir, upOptions{
		ConfigPath:     configPath,
		PlanOnly:       true,
		StartupTimeout: defaultStartupTimeout,
	})
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeState {
		t.Fatalf("deployTerraform() error = %#v, want unsafe state", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("deploy ran Terraform for an unsafe plan path: %#v", runner.commands)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "keep" {
		t.Fatalf("plan symlink target changed: %q, %v", got, readErr)
	}
}

func TestDeployDoesNotApplyAnEmptyPlan(t *testing.T) {
	dir := t.TempDir()
	configPath := writeTestDeployConfig(t, dir)
	runner := &recordingRunner{results: []CommandResult{
		{},
		{},
		{},
		{Stdout: "default\n"},
		{ExitCode: 0, Stdout: "No changes."},
		{Stdout: testTerraformOutputsJSON()},
		{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"},
		{},
	}}
	var out bytes.Buffer

	err := deployTerraform(context.Background(), &bytes.Buffer{}, &out, runner, dir, upOptions{
		ConfigPath:     configPath,
		AutoApprove:    true,
		StartupTimeout: defaultStartupTimeout,
	})
	if err != nil {
		t.Fatalf("deployTerraform() returned an error: %v", err)
	}
	initIndex, workspaceIndex := -1, -1
	for index, command := range runner.commands {
		if len(command.Args) == 0 {
			continue
		}
		switch command.Args[0] {
		case "init":
			initIndex = index
		case "workspace":
			workspaceIndex = index
		}
	}
	if initIndex == -1 || workspaceIndex == -1 || initIndex >= workspaceIndex {
		t.Fatalf("deploy did not reconfigure the local backend before workspace inspection: %#v", runner.commands)
	}
	for _, command := range runner.commands {
		if len(command.Args) > 0 && command.Args[0] == "apply" {
			t.Fatalf("deploy applied an empty plan: %#v", runner.commands)
		}
	}
	if !strings.Contains(out.String(), "Mutable Docker tags and GitHub branch contents are not refreshed automatically") {
		t.Fatalf("empty-plan output omitted source refresh limitation: %q", out.String())
	}
}

func writeTestDeployConfig(t *testing.T, dir string) string {
	t.Helper()
	data, err := json.Marshal(testDeployConfig())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "test-config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

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
	createTestSymlink(t, target, filepath.Join(dir, "terraform.tfstate"))

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

func TestDestroyRejectsSymlinkedPlanBeforeRunningTerraform(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	createTestSymlink(t, target, filepath.Join(dir, destroyPlanName))
	runner := &recordingRunner{}

	err := destroyTerraform(context.Background(), bytes.NewBufferString("yes\n"), &bytes.Buffer{}, runner, dir, downOptions{})
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeDestroy {
		t.Fatalf("destroyTerraform() error = %#v, want unsafe destroy", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("destroy ran Terraform for an unsafe plan path: %#v", runner.commands)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "keep" {
		t.Fatalf("plan symlink target changed: %q, %v", got, readErr)
	}
}

func createTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func TestDestroyRefusesANonDefaultWorkspaceBeforeReadingState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{results: []CommandResult{{}, {}, {}, {Stdout: "production\n"}}}

	err := destroyTerraform(context.Background(), bytes.NewBufferString("yes\n"), &bytes.Buffer{}, runner, dir, downOptions{})
	var deployErr *DeploymentError
	if !errors.As(err, &deployErr) || deployErr.Kind != FailureUnsafeDestroy {
		t.Fatalf("unexpected error: %#v", err)
	}
	wantOrder := []string{"fmt", "init", "validate", "workspace"}
	if len(runner.commands) != len(wantOrder) {
		t.Fatalf("destroy commands = %#v, want %v", runner.commands, wantOrder)
	}
	for i, want := range wantOrder {
		if runner.commands[i].Args[0] != want {
			t.Fatalf("destroy command %d = %#v, want %q", i, runner.commands[i], want)
		}
	}
}

func TestDestroyContinuesForVerifiedPartialStateWithoutVMOutputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeTerraformVariables(dir, testDeployConfig()); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{results: []CommandResult{
		{},
		{},
		{},
		{Stdout: "default\n"},
		{Stdout: "google_compute_network.demo\ngoogle_compute_subnetwork.demo\ngoogle_compute_firewall.http\ngoogle_compute_firewall.iap_ssh\n"},
		{Stdout: partialTerraformStateJSON()},
		{Stdout: "Destroy plan", ExitCode: 2},
	}}
	var out bytes.Buffer

	err := destroyTerraform(context.Background(), bytes.NewBufferString("no\n"), &out, runner, dir, downOptions{})
	if err != nil {
		t.Fatalf("destroyTerraform() returned an error after a declined confirmation: %v", err)
	}
	if !strings.Contains(out.String(), "VM: not created") {
		t.Fatalf("partial destroy summary = %q", out.String())
	}
	if !strings.Contains(out.String(), "Destroy cancelled; no resources were deleted.") {
		t.Fatalf("destroy cancellation output = %q", out.String())
	}
	for _, command := range runner.commands {
		if len(command.Args) >= 2 && command.Args[0] == "output" {
			t.Fatalf("partial destroy depended on terraform output: %#v", runner.commands)
		}
	}
	wantOrder := []string{"fmt", "init", "validate", "workspace", "state", "show", "plan"}
	if len(runner.commands) != len(wantOrder) {
		t.Fatalf("destroy commands = %#v, want %v", runner.commands, wantOrder)
	}
	for i, want := range wantOrder {
		if len(runner.commands[i].Args) == 0 || runner.commands[i].Args[0] != want {
			t.Fatalf("destroy command %d = %#v, want %q", i, runner.commands[i], want)
		}
	}
}

func partialTerraformStateJSON() string {
	return `{
		"format_version":"1.0",
		"values":{"root_module":{"resources":[
			{"address":"google_compute_network.demo","type":"google_compute_network","values":{"name":"gcp-free-deploy-network","project":"demo-project-123"}},
			{"address":"google_compute_subnetwork.demo","type":"google_compute_subnetwork","values":{"name":"gcp-free-deploy-subnet","project":"demo-project-123","region":"us-central1"}},
			{"address":"google_compute_firewall.http","type":"google_compute_firewall","values":{"name":"gcp-free-deploy-allow-http","project":"demo-project-123"}},
			{"address":"google_compute_firewall.iap_ssh","type":"google_compute_firewall","values":{"name":"gcp-free-deploy-allow-iap-ssh","project":"demo-project-123"}}
		]}}
	}`
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
		DockerImage:         "nginx:1.30.4",
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
