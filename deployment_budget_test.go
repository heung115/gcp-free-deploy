package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDeployBudgetFailureStopsBeforeApply(t *testing.T) {
	dir := t.TempDir()
	configPath := writeTestDeployConfig(t, dir)
	runner := &recordingRunner{results: []CommandResult{{}, {}, {}, {Stdout: "default\n"}, {Stdout: `[]`}, {ExitCode: 2}, {ExitCode: 1}}}
	err := deployTerraform(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, runner, dir, upOptions{ConfigPath: configPath, AutoApprove: true})
	if err == nil || !strings.Contains(err.Error(), "automatic cost alerts") {
		t.Fatalf("expected alert setup failure, got %v", err)
	}
	for _, cmd := range runner.commands {
		if cmd.Name == "terraform" && len(cmd.Args) > 0 && cmd.Args[0] == "apply" {
			t.Fatal("applied without cost alerts")
		}
	}
}

func TestDeployDeclinedOrPlanOnlyDoesNotConfigureBudget(t *testing.T) {
	for _, planOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "declined", true: "plan-only"}[planOnly], func(t *testing.T) {
			dir := t.TempDir()
			path := writeTestDeployConfig(t, dir)
			results := []CommandResult{{}, {}, {}, {Stdout: "default\n"}}
			if !planOnly {
				results = append(results, CommandResult{Stdout: `[]`})
			}
			results = append(results, CommandResult{ExitCode: 2})
			r := &recordingRunner{results: results}
			err := deployTerraform(context.Background(), strings.NewReader("no\n"), &bytes.Buffer{}, r, dir, upOptions{ConfigPath: path, PlanOnly: planOnly})
			if err != nil {
				t.Fatal(err)
			}
			for _, cmd := range r.commands {
				if cmd.Name == "gcloud" && len(cmd.Args) > 0 && cmd.Args[0] != "compute" {
					t.Fatalf("alert side effect before approval: %v", cmd)
				}
			}
		})
	}
}

func TestUpBudgetAlertsDefaultOnAndExplicitOptOut(t *testing.T) {
	for _, skip := range []bool{false, true} {
		args := []string{}
		if skip {
			args = append(args, "--skip-budget-alerts")
		}
		opts, err := parseUpOptions(args, &bytes.Buffer{})
		if err != nil || opts.SkipBudgetAlerts != skip {
			t.Fatalf("skip=%v opts=%+v err=%v", skip, opts, err)
		}
	}
}

func TestDeployAutomaticallyConfiguresBudgetOnChangedAndUnchangedPlans(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "changed"}[changed], func(t *testing.T) {
			dir := t.TempDir()
			path := writeTestDeployConfig(t, dir)
			alert := autoRunner("[]", "[]")
			alert.results = append(alert.results, CommandResult{Stdout: autoChannel})
			appendBudgetResults(alert, "[]", autoBudgetFixture("1"))
			for i := range alert.results {
				alert.results[i].Stdout = strings.ReplaceAll(alert.results[i].Stdout, "demo-project", "demo-project-123")
			}
			results := []CommandResult{{}, {}, {}, {Stdout: "default\n"}, {Stdout: `[]`}, {}}
			tier := CommandResult{Stdout: `{"name":"gcp-free-deploy-demo","zone":"zones/us-central1-a","networkInterfaces":[{"accessConfigs":[{"networkTier":"STANDARD"}]}]}`}
			if changed {
				results[len(results)-1].ExitCode = 2
				results = append(results, alert.results...)
				results = append(results, CommandResult{}, CommandResult{Stdout: testTerraformOutputsJSON()}, tier)
			} else {
				results = append(results, CommandResult{Stdout: testTerraformOutputsJSON()}, tier)
				results = append(results, alert.results...)
			}
			results = append(results, CommandResult{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"}, CommandResult{})
			r := &recordingRunner{results: results}
			if err := deployTerraform(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, r, dir, upOptions{ConfigPath: path, AutoApprove: true, StartupTimeout: defaultStartupTimeout}); err != nil {
				t.Fatal(err)
			}
			budgetIndex, applyIndex := -1, -1
			for i, c := range r.commands {
				if strings.Contains(strings.Join(c.Args, " "), "budgets create") {
					budgetIndex = i
				}
				if c.Name == "terraform" && len(c.Args) > 0 && c.Args[0] == "apply" {
					applyIndex = i
				}
			}
			if budgetIndex < 0 || (changed && applyIndex <= budgetIndex) || (!changed && applyIndex >= 0) {
				t.Fatalf("budget=%d apply=%d", budgetIndex, applyIndex)
			}
		})
	}
}
