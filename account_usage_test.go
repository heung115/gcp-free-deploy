package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

const usageAccount = "ABCDEF-123456-ABCDEF"
const usageProjects = `[{"projectId":"existing-project","billingAccountName":"billingAccounts/ABCDEF-123456-ABCDEF","billingEnabled":true}]`

func usageRunner() *recordingRunner {
	return &recordingRunner{results: []CommandResult{{Stdout: usageProjects}, {Stdout: `[]`}, {Stdout: `[]`}, {Stdout: `[]`}}}
}
func TestAccountScanFindsOtherProjectResourcesAndStoppedVM(t *testing.T) {
	r := usageRunner()
	r.results[1].Stdout = `[{"name":"old-vault","machineType":"zones/us-central1-a/machineTypes/e2-micro","zone":"zones/us-central1-a","status":"TERMINATED","networkInterfaces":[{"accessConfigs":[{"networkTier":"STANDARD"}]}]}]`
	r.results[2].Stdout = `[{"name":"leftover","type":"zones/us-central1-a/diskTypes/pd-standard","sizeGb":"30","users":[]}]`
	r.results[3].Stdout = `[{"name":"reserved-ip","addressType":"EXTERNAL","status":"RESERVED"}]`
	var out bytes.Buffer
	err := checkAccountFreeUsage(context.Background(), r, &out, usageAccount)
	if err == nil || !strings.Contains(err.Error(), "existing resources") {
		t.Fatal(err)
	}
	for _, s := range []string{"existing-project", "old-vault", "TERMINATED", "30 GB", "Reserved external IPs: 1"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal(out.String())
		}
	}
	for _, c := range r.commands {
		s := strings.Join(c.Args, " ")
		if strings.Contains(s, " enable") || strings.Contains(s, " create") || strings.Contains(s, " delete") {
			t.Fatal(c)
		}
	}
}
func TestAccountScanUnknownNeverReportedAsUnused(t *testing.T) {
	for index := 0; index < 4; index++ {
		r := usageRunner()
		r.results[index] = CommandResult{ExitCode: 1}
		var out bytes.Buffer
		if err := checkAccountFreeUsage(context.Background(), r, &out, usageAccount); err == nil {
			t.Fatal(index)
		}
		if strings.Contains(out.String(), "No current VM") {
			t.Fatal("unknown treated as empty")
		}
	}
	for _, raw := range []string{"null", "{}", "broken"} {
		r := usageRunner()
		r.results[0].Stdout = raw
		if err := checkAccountFreeUsage(context.Background(), r, &bytes.Buffer{}, usageAccount); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestAccountScanEmptyIsNotFreeBalanceClaim(t *testing.T) {
	r := usageRunner()
	var out bytes.Buffer
	if err := checkAccountFreeUsage(context.Background(), r, &out, usageAccount); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not a monthly free-quota balance") {
		t.Fatal(out.String())
	}
}
func TestAccountRiskRequiresExplicitAcknowledgement(t *testing.T) {
	for _, answer := range []string{"", "yes", "allow-cost-risk"} {
		r := usageRunner()
		r.results[0] = CommandResult{ExitCode: 1}
		err := reviewAccountFreeUsage(context.Background(), r, &bytes.Buffer{}, usageAccount, func(string) (string, error) { return answer, nil })
		if (err == nil) != (answer == "allow-cost-risk") {
			t.Fatalf("%q: %v", answer, err)
		}
	}
}
func TestWizardUsageRiskStopsBeforeLocalOrCloudCreation(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", t.TempDir())
	r := wizardRunner()
	r.results[4] = CommandResult{ExitCode: 1}
	var out bytes.Buffer
	err := runWizard(context.Background(), strings.NewReader("nginx:1.30.4\nno\n"), &out, &out, r, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "account cost check") {
		t.Fatal(err)
	}
	if len(r.commands) != 5 {
		t.Fatal(fmt.Sprint(r.commands))
	}
}
