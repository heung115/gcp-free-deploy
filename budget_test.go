package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const budgetFixture = `{"name":"billingAccounts/ABCDEF-123456-ABCDEF/budgets/one","displayName":"gcp-free-deploy-demo-project-cost-alert","amount":{"specifiedAmount":{"currencyCode":"USD","units":"1"}},"budgetFilter":{"projects":["projects/123456"],"calendarPeriod":"MONTH","creditTypesTreatment":"INCLUDE_ALL_CREDITS"},"thresholdRules":[{"thresholdPercent":0.01,"spendBasis":"CURRENT_SPEND"},{"thresholdPercent":0.1,"spendBasis":"CURRENT_SPEND"},{"thresholdPercent":1,"spendBasis":"CURRENT_SPEND"}],"notificationsRule":{}}`

func budgetRunner(api, existing string) *recordingRunner {
	return &recordingRunner{results: []CommandResult{
		{Stdout: `{"projectNumber":"123456"}`},
		{Stdout: `{"billingAccountName":"billingAccounts/ABCDEF-123456-ABCDEF","billingEnabled":true}`},
		{Stdout: api}, {Stdout: existing}, {Stdout: budgetFixture},
	}}
}
func runBudgetTest(r *recordingRunner, extra ...string) (string, error) {
	args := append([]string{"budget", "--billing-account=ABCDEF-123456-ABCDEF", "--project=demo-project"}, extra...)
	var out bytes.Buffer
	err := runCLI(context.Background(), args, strings.NewReader(""), &out, &bytes.Buffer{}, r, "")
	return out.String(), err
}
func TestBudgetCreatesScopedCreditAwareEmailAlert(t *testing.T) {
	r := budgetRunner(budgetAPI, "[]")
	out, err := runBudgetTest(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Created and verified") || !strings.Contains(out, "not necessarily USD") {
		t.Fatal(out)
	}
	args := strings.Join(r.commands[len(r.commands)-1].Args, " ")
	for _, want := range []string{"billing budgets create", "--filter-projects=projects/123456", "--credit-types-treatment=include-all-credits", "--budget-amount=1", "--calendar-period=month", "--threshold-rule=percent=0.01,basis=current-spend", "--threshold-rule=percent=0.1,basis=current-spend", "--threshold-rule=percent=1,basis=current-spend"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s in %s", want, args)
		}
	}
	if strings.Contains(args, "disable-default") {
		t.Fatal("default email recipients disabled")
	}
}
func TestBudgetExistingMatchingIsIdempotent(t *testing.T) {
	r := budgetRunner(budgetAPI, "["+budgetFixture+"]")
	out, err := runBudgetTest(r)
	if err != nil || !strings.Contains(out, "No changes needed") || len(r.commands) != 4 {
		t.Fatalf("%s %v %#v", out, err, r.commands)
	}
}
func TestBudgetPreservesCustomAndDuplicateBudgets(t *testing.T) {
	for _, fixture := range []string{strings.Replace(budgetFixture, `"units":"1"`, `"units":"5"`, 1), strings.Replace(budgetFixture, `"notificationsRule":{}`, `"notificationsRule":{"disableDefaultIamRecipients":true}`, 1), strings.Replace(budgetFixture, `"creditTypesTreatment":"INCLUDE_ALL_CREDITS"`, `"creditTypesTreatment":"EXCLUDE_ALL_CREDITS"`, 1), budgetFixture + "," + budgetFixture} {
		r := budgetRunner(budgetAPI, "["+fixture+"]")
		if _, err := runBudgetTest(r); err == nil {
			t.Fatal("accepted differing/duplicate budget")
		}
		if len(r.commands) != 4 {
			t.Fatal("modified existing budget")
		}
	}
}
func TestBudgetDryRunNeverMutates(t *testing.T) {
	for _, api := range []string{budgetAPI, ""} {
		r := budgetRunner(api, "[]")
		out, err := runBudgetTest(r, "--dry-run", "--enable-api")
		if err != nil || !strings.Contains(out, "Dry run") {
			t.Fatalf("%s %v", out, err)
		}
		for _, cmd := range r.commands {
			if strings.Contains(strings.Join(cmd.Args, " "), " create ") || strings.Contains(strings.Join(cmd.Args, " "), " enable ") {
				t.Fatal(cmd)
			}
		}
	}
}
func TestBudgetRequiresAPIEnableOptIn(t *testing.T) {
	r := budgetRunner("", "[]")
	if _, err := runBudgetTest(r); err == nil || !strings.Contains(err.Error(), "--enable-api") {
		t.Fatal(err)
	}
	if len(r.commands) != 3 {
		t.Fatal(r.commands)
	}
	r = budgetRunner("", "[]")
	r.results = append(r.results[:3], CommandResult{}, CommandResult{Stdout: "[]"}, CommandResult{Stdout: budgetFixture})
	if _, err := runBudgetTest(r, "--enable-api"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.commands[3].Args, " "), "services enable "+budgetAPI) {
		t.Fatal(r.commands)
	}
}
func TestBudgetRejectsWrongAccountAndBadResults(t *testing.T) {
	for _, index := range []int{0, 1, 2, 3, 4} {
		r := budgetRunner(budgetAPI, "[]")
		r.results[index] = CommandResult{Stdout: "invalid"}
		if _, err := runBudgetTest(r); err == nil {
			t.Fatalf("accepted invalid response %d", index)
		}
	}
	r := budgetRunner(budgetAPI, "[]")
	r.results[1].Stdout = `{"billingAccountName":"billingAccounts/000000-000000-000000","billingEnabled":true}`
	if _, err := runBudgetTest(r); err == nil || len(r.commands) != 2 {
		t.Fatalf("%v %v", err, r.commands)
	}
	r = budgetRunner(budgetAPI, "[]")
	r.results[3] = CommandResult{ExitCode: 1}
	if _, err := runBudgetTest(r); err == nil || len(r.commands) != 4 {
		t.Fatalf("%v %v", err, r.commands)
	}
}
func TestBudgetOptionsValidatedBeforeCommands(t *testing.T) {
	for _, extra := range [][]string{{"--amount=0"}, {"--amount=-1"}, {"--amount=NaN"}, {"--amount=1USD"}, {"--amount=1.0000000001"}, {"--amount=1000000000"}, {"unexpected"}, {"--billing-account="}, {"--project="}, {"--notification-channel=bad"}} {
		r := &recordingRunner{}
		if _, err := runBudgetTest(r, extra...); err == nil || len(r.commands) != 0 {
			t.Fatalf("%v %v", extra, err)
		}
	}
	r := &recordingRunner{}
	if _, err := runBudgetTest(r, "--help"); err != nil || len(r.commands) != 0 {
		t.Fatal(err)
	}
}
func TestBudgetNotificationChannelAndExactMoney(t *testing.T) {
	var b billingBudget
	if err := json.Unmarshal([]byte(budgetFixture), &b); err != nil {
		t.Fatal(err)
	}
	b.Amount.SpecifiedAmount.Units = ""
	b.Amount.SpecifiedAmount.Nanos = 10000000
	if !b.matches("123456", 10000000, "") {
		t.Fatal("fractional amount did not match")
	}
	channel := "projects/demo-project/notificationChannels/123"
	b.NotificationsRule.MonitoringNotificationChannels = []string{channel}
	if !b.matches("123456", 10000000, channel) || b.matches("123456", 10000000, "") {
		t.Fatal("channel not compared")
	}
	b.Amount.SpecifiedAmount.Units = "1"
	b.Amount.SpecifiedAmount.Nanos = 0
	data, _ := json.Marshal(b)
	r := budgetRunner(budgetAPI, "[]")
	r.results[4].Stdout = string(data)
	if _, err := runBudgetTest(r, "--notification-channel="+channel); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.commands[4].Args, " "), "--notifications-rule-monitoring-notification-channels="+channel) {
		t.Fatal(r.commands[4])
	}
}

func TestBudgetRejectsRestrictedScopeAndChangedThresholds(t *testing.T) {
	for _, field := range []string{`"services":["services/123"]`, `"labels":{"env":["dev"]}`, `"subaccounts":["billingAccounts/other"]`, `"resourceAncestors":["folders/123"]`} {
		fixture := strings.Replace(budgetFixture, `"calendarPeriod":"MONTH"`, `"calendarPeriod":"MONTH",`+field, 1)
		r := budgetRunner(budgetAPI, "["+fixture+"]")
		if _, err := runBudgetTest(r); err == nil || len(r.commands) != 4 {
			t.Fatalf("accepted restricted scope %s: %v", field, err)
		}
	}
	fixture := strings.Replace(budgetFixture, `"thresholdPercent":0.01`, `"thresholdPercent":0.1`, 1)
	r := budgetRunner(budgetAPI, "["+fixture+"]")
	if _, err := runBudgetTest(r); err == nil {
		t.Fatal("accepted duplicate/missing low threshold")
	}
}

func TestBudgetQuotaProjectOnlyForBudgetAPICalls(t *testing.T) {
	r := budgetRunner(budgetAPI, "[]")
	if _, err := runBudgetTest(r); err != nil {
		t.Fatal(err)
	}
	for _, command := range r.commands {
		budgetCall := len(command.Args) >= 2 && command.Args[0] == "billing" && command.Args[1] == "budgets"
		hasQuotaProject := false
		for _, arg := range command.Args {
			if arg == "--billing-project=demo-project" {
				hasQuotaProject = true
			}
		}
		if hasQuotaProject != budgetCall {
			t.Fatalf("quota project flag incorrectly placed: %v", command.Args)
		}
	}
}
