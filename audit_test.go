package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func auditFixture(tier string) []CommandResult {
	return []CommandResult{
		{Stdout: `{"name":"my-free-portfolio","machineType":"zones/us-central1-a/machineTypes/e2-micro","zone":"zones/us-central1-a","status":"RUNNING","disks":[{"source":"projects/example-project/zones/us-central1-a/disks/boot"}],"networkInterfaces":[{"accessConfigs":[{"networkTier":"` + tier + `"}]}]}`},
		{Stdout: `[{"name":"boot","type":"zones/us-central1-a/diskTypes/pd-standard","zone":"zones/us-central1-a","sizeGb":"30","users":["my-free-portfolio"]}]`},
		{Stdout: `[{"name":"my-free-portfolio","machineType":"e2-micro","zone":"zones/us-central1-a","status":"RUNNING"}]`},
	}
}

func TestAuditChecksActualNetworkTierWithoutMutations(t *testing.T) {
	for _, tier := range []string{"PREMIUM", "STANDARD", ""} {
		t.Run(tier, func(t *testing.T) {
			runner := &recordingRunner{results: auditFixture(tier)}
			var out, errOut bytes.Buffer
			workdir := t.TempDir()
			err := runCLI(context.Background(), []string{"audit", "--project", "example-project", "--vm", "my-free-portfolio", "--zone", "us-central1-a"}, strings.NewReader(""), &out, &errOut, runner, workdir)
			if (err == nil) != (tier == "STANDARD") {
				t.Fatalf("tier %q: err=%v; output=%s", tier, err, out.String())
			}
			if err != nil {
				var de *DeploymentError
				if !errors.As(err, &de) || de.Kind != FailureKind("cost_audit_profile") {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if !strings.Contains(out.String(), "Not a zero-cost guarantee") {
				t.Fatal("missing limits")
			}
			if len(runner.commands) != 3 {
				t.Fatalf("commands: %v", runner.commands)
			}
			for _, cmd := range runner.commands {
				if cmd.Name != "gcloud" || cmd.Args[0] != "compute" || (cmd.Args[2] != "describe" && cmd.Args[2] != "list") {
					t.Fatalf("unexpected mutation: %v", cmd)
				}
				if !strings.Contains(strings.Join(cmd.Args, " "), "--project=example-project") {
					t.Fatal("implicit project")
				}
				if strings.Contains(strings.Join(cmd.Args, " "), "natIP") {
					t.Fatal("unneeded IP read")
				}
			}
			entries, e := os.ReadDir(workdir)
			if e != nil || len(entries) != 0 {
				t.Fatalf("audit wrote local state: %v %v", entries, e)
			}
		})
	}
}

func TestAuditRequiresExplicitValidTarget(t *testing.T) {
	for _, args := range [][]string{nil, {"--project", "example-project", "--vm", "my-free-portfolio"}, {"--project", "example-project", "--vm", "--help", "--zone", "us-central1-a"}, {"--project", "example-project;echo hi", "--vm", "my-free-portfolio", "--zone", "us-central1-a"}} {
		runner := &recordingRunner{}
		var out bytes.Buffer
		if err := auditLiveCost(context.Background(), args, &out, &out, runner); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if len(runner.commands) != 0 {
			t.Fatal("queried with invalid target")
		}
	}
}

func TestAuditStopsOnFailedOrMalformedQuery(t *testing.T) {
	for _, result := range []CommandResult{{ExitCode: 1, Stderr: "private diagnostic"}, {Stdout: "broken json"}} {
		runner := &recordingRunner{results: []CommandResult{result}}
		var out bytes.Buffer
		err := auditLiveCost(context.Background(), []string{"--project", "example-project", "--vm", "my-free-portfolio", "--zone", "us-central1-a"}, &out, &out, runner)
		if err == nil || len(runner.commands) != 1 {
			t.Fatalf("did not stop: %v / %v", err, runner.commands)
		}
		if strings.Contains(err.Error(), "private diagnostic") {
			t.Fatal("leaked diagnostic")
		}
	}
}

func TestAuditRejectsPaidAndUnverifiedResources(t *testing.T) {
	for _, test := range []struct {
		name     string
		index    int
		old, new string
	}{
		{"machine", 0, "e2-micro", "e2-small"},
		{"region", 0, "us-central1-a", "asia-northeast3-a"},
		{"disk-type", 1, "pd-standard", "pd-balanced"},
		{"disk-size", 1, `"30"`, `"40"`},
		{"orphan", 1, `["my-free-portfolio"]`, `[]`},
		{"missing-disk", 1, `"boot"`, `"other"`},
		{"missing-target", 2, `"my-free-portfolio"`, `"other"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := auditFixture("STANDARD")
			results[test.index].Stdout = strings.ReplaceAll(results[test.index].Stdout, test.old, test.new)
			runner := &recordingRunner{results: results}
			var out bytes.Buffer
			if err := auditLiveCost(context.Background(), []string{"--project", "example-project", "--vm", "my-free-portfolio", "--zone", "us-central1-a"}, &out, &out, runner); err == nil {
				t.Fatalf("accepted invalid profile: %s", out.String())
			}
		})
	}
}

func TestAuditUsesArrayProjectionsAndDoesNotAssumeMissingMeansEmpty(t *testing.T) {
	for _, access := range []string{`null`, `[]`} {
		results := auditFixture("STANDARD")
		results[0].Stdout = strings.Replace(results[0].Stdout, `[{"networkTier":"STANDARD"}]`, access, 1)
		runner := &recordingRunner{results: results}
		var out bytes.Buffer
		err := auditLiveCost(context.Background(), []string{"--project", "example-project", "--vm", "my-free-portfolio", "--zone", "us-central1-a"}, &out, &out, runner)
		if (err == nil) != (access == `[]`) {
			t.Fatalf("access=%s err=%v", access, err)
		}
		if strings.Contains(out.String(), "no external IPv4") != (access == `[]`) {
			t.Fatalf("false absence claim: %s", out.String())
		}
		args := strings.Join(runner.commands[0].Args, " ")
		if !strings.Contains(args, "disks[].source") || !strings.Contains(args, "networkInterfaces[].accessConfigs[].networkTier") {
			t.Fatalf("broken projections: %s", args)
		}
	}
}

func TestDeployVerifiesActualTierBeforeSuccess(t *testing.T) {
	for _, changed := range []bool{false, true} {
		for _, override := range []bool{false, true} {
			dir := t.TempDir()
			configPath := writeTestDeployConfig(t, dir)
			results := []CommandResult{{}, {}, {}, {Stdout: "default\n"}}
			if !override {
				results = append(results, CommandResult{Stdout: `[]`})
			}
			results = append(results, CommandResult{})
			if changed {
				results[len(results)-1].ExitCode = 2
				results = append(results, CommandResult{})
			}
			results = append(results, CommandResult{Stdout: testTerraformOutputsJSON()})
			if !override {
				results = append(results, CommandResult{Stdout: `{"name":"gcp-free-deploy-demo","zone":"zones/us-central1-a","networkInterfaces":[{"accessConfigs":[{"networkTier":"PREMIUM"}]}]}`})
			}
			results = append(results, CommandResult{Stdout: "STARTUP_DONE\nCONTAINER_RUNNING\nHTTP_HEALTH_OK"}, CommandResult{})
			runner := &recordingRunner{results: results}
			var out bytes.Buffer
			err := deployTerraform(context.Background(), &bytes.Buffer{}, &out, runner, dir, upOptions{ConfigPath: configPath, AutoApprove: true, AllowPaidResources: override, SkipBudgetAlerts: true, StartupTimeout: defaultStartupTimeout})
			if (err == nil) != override {
				t.Fatalf("changed=%v override=%v err=%v output=%s", changed, override, err, out.String())
			}
			if !override {
				if !strings.Contains(err.Error(), "VM and network remain") {
					t.Fatalf("missing cleanup hint: %v", err)
				}
				for _, cmd := range runner.commands {
					if cmd.Name == "http-get" || (cmd.Name == "gcloud" && len(cmd.Args) > 1 && cmd.Args[1] == "ssh") {
						t.Fatalf("monitor ran before tier verification: %v", cmd)
					}
				}
			}
		}
	}
}

func TestVerifyLiveNetworkTierRejectsUnknownOrFailedResponses(t *testing.T) {
	for _, result := range []CommandResult{{ExitCode: 1}, {Stdout: `{}`}, {Stdout: `{"name":"gcp-free-deploy-demo","zone":"zones/us-central1-a","networkInterfaces":[{"accessConfigs":null}]}`}, {Stdout: `{"name":"gcp-free-deploy-demo","zone":"zones/us-central1-a","networkInterfaces":[{"accessConfigs":[{}]}]}`}} {
		runner := &recordingRunner{results: []CommandResult{result}}
		if err := verifyLiveNetworkTier(context.Background(), runner, testTerraformOutputs()); err == nil {
			t.Fatalf("accepted unknown response: %v", result)
		}
	}
}

func TestGuardProjectVMOverlap(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		fails     bool
	}{
		{"empty", `[]`, false},
		{"managed", `[{"name":"gcp-free-deploy-demo","zone":"zones/us-central1-a","status":"RUNNING"}]`, false},
		{"fallback", `[{"name":"gcp-free-deploy-demo","zone":"zones/us-central1-b","status":"RUNNING"}]`, false},
		{"legacy", `[{"name":"my-free-portfolio","zone":"zones/us-central1-a","status":"RUNNING"}]`, true},
		{"stopped", `[{"name":"other","zone":"zones/us-central1-a","status":"TERMINATED"}]`, true},
		{"wrong-zone", `[{"name":"gcp-free-deploy-demo","zone":"zones/us-east1-b","status":"RUNNING"}]`, true},
		{"null", `null`, true},
		{"missing", `[{}]`, true},
		{"invalid", `bad`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testDeployConfig()
			cfg.FallbackZones = []string{"us-central1-b"}
			runner := &recordingRunner{results: []CommandResult{{Stdout: test.raw}}}
			err := guardProjectVMOverlap(context.Background(), runner, cfg)
			if (err != nil) != test.fails {
				t.Fatalf("err=%v", err)
			}
			if len(runner.commands) != 1 || !strings.Contains(strings.Join(runner.commands[0].Args, " "), "instances list --project=demo-project-123") {
				t.Fatalf("unexpected query: %v", runner.commands)
			}
		})
	}
}

func TestUpStopsBeforePlanWhenLegacyVMExistsOrQueryFails(t *testing.T) {
	for _, result := range []CommandResult{{Stdout: `[{"name":"my-free-portfolio","zone":"zones/us-central1-a","status":"RUNNING"}]`}, {ExitCode: 1}} {
		dir := t.TempDir()
		configPath := writeTestDeployConfig(t, dir)
		runner := &recordingRunner{results: []CommandResult{{}, {}, {}, {Stdout: "default\n"}, result}}
		err := deployTerraform(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, runner, dir, upOptions{ConfigPath: configPath, AutoApprove: true, StartupTimeout: defaultStartupTimeout})
		if err == nil || !strings.Contains(err.Error(), "project VM overlap") {
			t.Fatalf("missing overlap rejection: %v", err)
		}
		for _, cmd := range runner.commands {
			if len(cmd.Args) > 0 && (cmd.Args[0] == "plan" || cmd.Args[0] == "apply") {
				t.Fatalf("reached deployment despite existing VM: %v", cmd)
			}
		}
	}
}
