package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wizardRunner() *recordingRunner {
	return &recordingRunner{results: []CommandResult{
		{Stdout: "owner@example.com"}, {Stdout: "SECRET_ADC_TOKEN"},
		{Stdout: `[{"projectId":"demo-project-123"}]`},
		{Stdout: `{"billingEnabled":true,"billingAccountName":"billingAccounts/ABCDEF-123456-ABCDEF"}`},
		{Stdout: `[{"projectId":"demo-project-123","billingAccountName":"billingAccounts/ABCDEF-123456-ABCDEF","billingEnabled":true}]`},
		{Stdout: `[]`}, {Stdout: `[]`}, {Stdout: `[]`},
		{Stdout: "8.8.8.8"},
	}}
}

func TestWizardEOFAndHelpDoNotRunTools(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	for _, args := range [][]string{nil, {"start"}, {"start", "--help"}} {
		r := &recordingRunner{}
		var out bytes.Buffer
		if err := runCLI(context.Background(), args, strings.NewReader(""), &out, &out, r, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if len(r.commands) != 0 {
			t.Fatal(r.commands)
		}
	}
}

func TestWizardCancelDoesNotWriteOrEnableAPI(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	dir := t.TempDir()
	r := wizardRunner()
	var out bytes.Buffer
	err := runWizard(context.Background(), strings.NewReader("nginx:1.30.4\ncancel\n"), &out, &out, r, dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 || len(r.commands) != 9 {
		t.Fatalf("cancel mutated: files=%v commands=%v", entries, r.commands)
	}
	if strings.Contains(out.String(), "SECRET_ADC_TOKEN") {
		t.Fatal("credential leaked")
	}
}

func TestWizardCreatesIsolatedConfigThenHonorsPlanRejection(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	dir := t.TempDir()
	marker := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(marker, []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	r := wizardRunner()
	r.results = append(r.results, CommandResult{Stdout: "compute.googleapis.com"}, CommandResult{}, CommandResult{}, CommandResult{}, CommandResult{Stdout: "default\n"}, CommandResult{Stdout: `[]`}, CommandResult{ExitCode: 2})
	var out bytes.Buffer
	if err := runWizard(context.Background(), strings.NewReader("nginx:1.30.4\n\nno\n"), &out, &out, r, dir); err != nil {
		t.Fatal(err)
	}
	configs, _ := filepath.Glob(filepath.Join(dir, "gcp-deploy-*", "gcp-free-deploy.json"))
	if len(configs) != 1 {
		t.Fatal(configs)
	}
	cfg, err := LoadDeployConfig(configs[0])
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRuntimeHours != 24 || cfg.AllowedSourceRanges[0] != "8.8.8.8/32" || cfg.ProjectID != "demo-project-123" {
		t.Fatalf("%+v", cfg)
	}
	data, _ := os.ReadFile(marker)
	if string(data) != "user data" {
		t.Fatal("existing file overwritten")
	}
	for _, c := range r.commands {
		s := strings.Join(c.Args, " ")
		if strings.Contains(s, "budgets create") || (c.Name == "terraform" && len(c.Args) > 0 && c.Args[0] == "apply") {
			t.Fatalf("ignored rejection: %v", c)
		}
	}
}

func TestWizardRejectsUnlinkedBillingBeforeIPOrWrites(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	r := wizardRunner()
	r.results[3].Stdout = `{"billingEnabled":false}`
	dir := t.TempDir()
	var out bytes.Buffer
	err := runWizard(context.Background(), strings.NewReader("nginx:1.30.4\n"), &out, &out, r, dir)
	if err == nil || !strings.Contains(err.Error(), "billing") {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 || len(r.commands) != 4 {
		t.Fatal("continued without billing")
	}
}

func TestWizardPublicIPv4RejectsBroadAndNonPublicAccess(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	for _, s := range []string{"0.0.0.0/0", "0.0.0.0", "127.0.0.1", "192.168.1.1", "100.64.0.1", "203.0.113.1", "::1", "invalid"} {
		if wizardPublicIPv4(s) {
			t.Fatal(s)
		}
	}
	if !wizardPublicIPv4("8.8.8.8") {
		t.Fatal("rejected public IPv4")
	}
}

type wizardInteractiveRunner struct {
	*recordingRunner
	logins int
}

func (r *wizardInteractiveRunner) RunInteractive(ctx context.Context, c Command, in io.Reader, out, errOut io.Writer) CommandResult {
	r.logins++
	if strings.Join(c.Args, " ") != "auth login --update-adc" {
		return CommandResult{ExitCode: 1}
	}
	return CommandResult{}
}
func TestWizardBrowserLoginRequiresConsentAndRechecksCredentials(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	for _, consent := range []string{"no", "yes"} {
		base := wizardRunner()
		base.results = append([]CommandResult{{Stdout: ""}}, base.results...)
		r := &wizardInteractiveRunner{recordingRunner: base}
		var out bytes.Buffer
		if err := runWizard(context.Background(), strings.NewReader("nginx:1.30.4\n"+consent+"\n"), &out, &out, r, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		want := 0
		if consent == "yes" {
			want = 1
		}
		if r.logins != want {
			t.Fatalf("consent=%s logins=%d", consent, r.logins)
		}
		if strings.Contains(out.String(), "SECRET_ADC_TOKEN") {
			t.Fatal("credential exposed")
		}
	}
}
func TestWizardManualIPFallbackAndContinuousGitHubMode(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	dir := t.TempDir()
	r := wizardRunner()
	r.results[8] = CommandResult{ExitCode: 1}
	r.results = append(r.results, CommandResult{Stdout: "compute.googleapis.com"}, CommandResult{ExitCode: 1})
	var out bytes.Buffer
	err := runWizard(context.Background(), strings.NewReader("https://github.com/example/demo.git\n8.8.4.4\nport\n8080\nruntime\n2\n\n"), &out, &out, r, dir)
	if err == nil {
		t.Fatal("expected simulated Terraform preflight failure")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "gcp-deploy-*", "gcp-free-deploy.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	cfg, e := LoadDeployConfig(files[0])
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Source != "github" || cfg.MaxRuntimeHours != 0 || cfg.ContainerPort != 8080 || cfg.AllowedSourceRanges[0] != "8.8.4.4/32" {
		t.Fatalf("%+v", cfg)
	}
}

func TestWizardManagesSavedDeploymentFromAnotherDirectory(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	dir := writeCatalogTestDeployment(t, t.TempDir())
	if err := rememberDeployment(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	// Fail during read-only preflight, after proving the selected target path.
	r := &recordingRunner{results: []CommandResult{{ExitCode: 1}}}
	var out bytes.Buffer
	err := runWizard(context.Background(), strings.NewReader("1\n2\n"), &out, &out, r, t.TempDir())
	if err == nil || len(r.commands) != 1 {
		t.Fatalf("%v %v", err, r.commands)
	}
	expected, _ := filepath.EvalSymlinks(dir)
	if r.commands[0].Dir != expected {
		t.Fatalf("wrong deletion target: %s", r.commands[0].Dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate")); err != nil {
		t.Fatal("state removed")
	}
}

func TestWizardMultipleProjectsStillRequiresSelection(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	r := wizardRunner()
	r.results[2].Stdout = `[{"projectId":"first-project"},{"projectId":"second-project"}]`
	r.results[4].Stdout = strings.ReplaceAll(r.results[4].Stdout, "demo-project-123", "second-project")
	r.missing = map[string]bool{"curl": true}
	var out bytes.Buffer
	if err := runWizard(context.Background(), strings.NewReader("nginx:1.30.4\n2\ncancel\n"), &out, &out, r, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.commands[3].Args, " "), "second-project") {
		t.Fatal("wrong project")
	}
	if !strings.Contains(out.String(), "Project number") {
		t.Fatal("silently chose among projects")
	}
}
