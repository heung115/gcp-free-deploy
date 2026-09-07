package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const autoChannel = `{"name":"projects/demo-project/notificationChannels/123","type":"email","enabled":true,"labels":{"email_address":"owner@example.com"}}`

func autoBudgetFixture(amount string) string {
	s := strings.Replace(budgetFixture, `"units":"1"`, `"units":"`+amount+`"`, 1)
	return strings.Replace(s, `"notificationsRule":{}`, `"notificationsRule":{"monitoringNotificationChannels":["projects/demo-project/notificationChannels/123"]}`, 1)
}
func autoRunner(existing, channels string) *recordingRunner {
	return &recordingRunner{results: []CommandResult{
		{Stdout: "owner@example.com\n"}, {Stdout: `{"projectNumber":"123456"}`}, {Stdout: `{"billingAccountName":"billingAccounts/ABCDEF-123456-ABCDEF","billingEnabled":true}`},
		{Stdout: budgetAPI}, {Stdout: monitoringAPI}, {Stdout: existing}, {Stdout: channels},
	}}
}
func appendBudgetResults(r *recordingRunner, existing, created string) {
	r.results = append(r.results, CommandResult{Stdout: `{"projectNumber":"123456"}`}, CommandResult{Stdout: `{"billingAccountName":"billingAccounts/ABCDEF-123456-ABCDEF","billingEnabled":true}`}, CommandResult{Stdout: budgetAPI}, CommandResult{Stdout: existing})
	if created != "" {
		r.results = append(r.results, CommandResult{Stdout: created})
	}
}
func TestAutomaticBudgetCreatesEmailAndBudget(t *testing.T) {
	r := autoRunner("[]", "[]")
	r.results = append(r.results, CommandResult{Stdout: autoChannel})
	appendBudgetResults(r, "[]", autoBudgetFixture("1"))
	var out bytes.Buffer
	if err := configureDeploymentBudget(context.Background(), &out, r, "demo-project"); err != nil {
		t.Fatal(err)
	}
	var emailCreated, budgetCreated bool
	for _, c := range r.commands {
		args := strings.Join(c.Args, " ")
		if strings.Contains(args, "channels create") {
			emailCreated = true
			if !strings.Contains(args, `"email_address":"owner@example.com"`) {
				t.Fatal(args)
			}
		}
		if strings.Contains(args, "budgets create") {
			budgetCreated = true
			if !strings.Contains(args, "--notifications-rule-monitoring-notification-channels=projects/demo-project/notificationChannels/123") {
				t.Fatal(args)
			}
		}
	}
	if !emailCreated || !budgetCreated {
		t.Fatal(r.commands)
	}
}
func TestAutomaticBudgetReusesExistingCustomAmount(t *testing.T) {
	fixture := autoBudgetFixture("100")
	r := autoRunner("["+fixture+"]", "["+autoChannel+"]")
	appendBudgetResults(r, "["+fixture+"]", "")
	if err := configureDeploymentBudget(context.Background(), &bytes.Buffer{}, r, "demo-project"); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.commands {
		if strings.Contains(strings.Join(c.Args, " "), " create") {
			t.Fatal(c)
		}
	}
}
func TestAutomaticBudgetReusesEmailChannel(t *testing.T) {
	r := autoRunner("[]", "["+autoChannel+"]")
	appendBudgetResults(r, "[]", autoBudgetFixture("1"))
	if err := configureDeploymentBudget(context.Background(), &bytes.Buffer{}, r, "demo-project"); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.commands {
		if strings.Contains(strings.Join(c.Args, " "), "channels create") {
			t.Fatal(c)
		}
	}
}
func TestAutomaticBudgetRefusesInvalidDiscoveryAndCustomAlerts(t *testing.T) {
	cases := []struct {
		index int
		value string
	}{
		{0, "robot@example.iam.gserviceaccount.com"}, {0, "(unset)"}, {1, "{}"}, {2, `{"billingEnabled":false}`}, {3, "other-api"}, {5, "null"}, {5, "[" + budgetFixture + "]"}, {6, "null"},
	}
	for _, tt := range cases {
		r := autoRunner("[]", "[]")
		r.results[tt.index].Stdout = tt.value
		if err := configureDeploymentBudget(context.Background(), &bytes.Buffer{}, r, "demo-project"); err == nil {
			t.Fatalf("accepted %d %s", tt.index, tt.value)
		}
		for _, c := range r.commands {
			if strings.Contains(strings.Join(c.Args, " "), " create") {
				t.Fatal(c)
			}
		}
	}
}
func TestAutomaticBudgetDoesNotReplaceDisabledOrDifferentOwnerChannel(t *testing.T) {
	for _, channel := range []string{strings.Replace(autoChannel, `"enabled":true`, `"enabled":false`, 1), strings.Replace(autoChannel, "owner@example.com", "someone@example.com", 1)} {
		r := autoRunner("["+autoBudgetFixture("100")+"]", "["+channel+"]")
		if err := configureDeploymentBudget(context.Background(), &bytes.Buffer{}, r, "demo-project"); err == nil {
			t.Fatal("accepted invalid existing channel")
		}
		for _, c := range r.commands {
			if strings.Contains(strings.Join(c.Args, " "), " create") {
				t.Fatal(c)
			}
		}
	}
}
func TestAutomaticBudgetEnablesMissingAPIs(t *testing.T) {
	r := autoRunner("[]", "["+autoChannel+"]")
	r.results = append(r.results[:3], append([]CommandResult{{}, {}, {}, {}}, r.results[5:]...)...)
	appendBudgetResults(r, "[]", autoBudgetFixture("1"))
	if err := configureDeploymentBudget(context.Background(), &bytes.Buffer{}, r, "demo-project"); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, c := range r.commands {
		if strings.Contains(strings.Join(c.Args, " "), "services enable") {
			count++
		}
	}
	if count != 2 {
		t.Fatal(r.commands)
	}
}
func TestAutomaticBudgetStopsAfterCommandFailure(t *testing.T) {
	r := autoRunner("[]", "[]")
	r.results[5] = CommandResult{ExitCode: 1}
	if err := configureDeploymentBudget(context.Background(), &bytes.Buffer{}, r, "demo-project"); err == nil {
		t.Fatal("accepted lookup failure")
	}
	if len(r.commands) != 6 {
		t.Fatal(r.commands)
	}
}
