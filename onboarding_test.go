package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type onboardingRunner struct {
	recordingRunner
	audited             bool
	mutationBeforeAudit bool
	createFailure       bool
	linkFailure         bool
	badLink             bool
}

func (r *onboardingRunner) Run(ctx context.Context, c Command) CommandResult {
	if len(c.Args) > 1 && c.Args[0] == "projects" && c.Args[1] == "create" {
		r.commands = append(r.commands, c)
		if !r.audited {
			r.mutationBeforeAudit = true
		}
		if r.createFailure {
			return CommandResult{ExitCode: 1, Stderr: "SECRET_ERROR"}
		}
		data, _ := json.Marshal(map[string]string{"projectId": c.Args[2]})
		return CommandResult{Stdout: string(data)}
	}
	if len(c.Args) > 2 && c.Args[0] == "billing" && c.Args[1] == "projects" && c.Args[2] == "link" {
		r.commands = append(r.commands, c)
		if !r.audited {
			r.mutationBeforeAudit = true
		}
		if r.linkFailure {
			return CommandResult{ExitCode: 1, Stderr: "SECRET_ERROR"}
		}
		account := strings.TrimPrefix(c.Args[4], "--billing-account=")
		if r.badLink {
			account = "000000-000000-000000"
		}
		data, _ := json.Marshal(map[string]any{"name": "projects/" + c.Args[3] + "/billingInfo", "billingAccountName": "billingAccounts/" + account, "billingEnabled": true})
		return CommandResult{Stdout: string(data)}
	}
	return r.recordingRunner.Run(ctx, c)
}
func onboardingFixture() *onboardingRunner {
	return &onboardingRunner{recordingRunner: recordingRunner{results: []CommandResult{{Stdout: `[{"name":"billingAccounts/ABCDEF-123456-ABCDEF","displayName":"Personal","open":true}]`}}}}
}
func onboardingAnswers(answers ...string) func(string) (string, error) {
	return func(string) (string, error) {
		if len(answers) == 0 {
			return "", io.EOF
		}
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
}
func TestProjectCreationRequiresApprovalAndAudit(t *testing.T) {
	for _, tc := range []struct {
		name          string
		answers       []string
		failAudit     bool
		expectedCalls int
	}{
		{"decline", []string{"no"}, false, 0},
		{"cancel final", []string{"yes", "", "no"}, false, 1},
		{"EOF final", []string{"yes", ""}, false, 1},
		{"existing usage", []string{"yes", "", "yes"}, true, 1},
		{"create", []string{"yes", "", "yes"}, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := onboardingFixture()
			var out bytes.Buffer
			id, err := proposeProjectCreation(context.Background(), r, &out, onboardingAnswers(tc.answers...), func(account string) error {
				if account != "ABCDEF-123456-ABCDEF" {
					t.Fatal(account)
				}
				r.audited = true
				if tc.failAudit {
					return errors.New("existing usage found")
				}
				return nil
			})
			if len(r.commands) != tc.expectedCalls || r.mutationBeforeAudit {
				t.Fatalf("calls=%v earlyMutation=%v", r.commands, r.mutationBeforeAudit)
			}
			if tc.expectedCalls == 3 {
				if err != nil || !projectIDPattern.MatchString(id) {
					t.Fatalf("id=%s err=%v", id, err)
				}
				if !strings.Contains(out.String(), id) {
					t.Fatal("project absent from summary")
				}
			} else if id != "" {
				t.Fatal(id)
			}
		})
	}
}
func TestProjectCreationFailurePreservesRecoveryAndRedactsDiagnostics(t *testing.T) {
	for _, failure := range []string{"create", "link", "wrong link"} {
		t.Run(failure, func(t *testing.T) {
			r := onboardingFixture()
			r.createFailure = failure == "create"
			r.linkFailure = failure == "link"
			r.badLink = failure == "wrong link"
			var out bytes.Buffer
			_, err := proposeProjectCreation(context.Background(), r, &out, onboardingAnswers("yes", "", "yes"), func(string) error { r.audited = true; return nil })
			if err == nil || strings.Contains(err.Error()+out.String(), "SECRET_ERROR") {
				t.Fatal(err)
			}
			project := r.commands[1].Args[2]
			if !strings.Contains(err.Error(), project) {
				t.Fatal("missing project recovery ID", err)
			}
			if failure == "create" && len(r.commands) != 2 {
				t.Fatal("linked after unconfirmed creation")
			}
			for _, c := range r.commands {
				if strings.Contains(strings.Join(c.Args, " "), "delete") {
					t.Fatal("project deleted")
				}
			}
		})
	}
}
func TestProjectCreationInvalidBillingStopsBeforeMutation(t *testing.T) {
	for _, payload := range []string{`[]`, `null`, `{}`, `[{"name":"billingAccounts/ABCDEF-123456-ABCDEF","open":false}]`, `[{"name":"invalid","open":true}]`} {
		r := onboardingFixture()
		r.results[0].Stdout = payload
		_, err := proposeProjectCreation(context.Background(), r, io.Discard, onboardingAnswers("yes", "", "yes"), func(string) error { t.Fatal("audit ran"); return nil })
		if err == nil || len(r.commands) != 1 {
			t.Fatalf("payload %s error %v commands %v", payload, err, r.commands)
		}
	}
}
func TestProjectCreationSelectsBillingAccount(t *testing.T) {
	r := onboardingFixture()
	r.results[0].Stdout = `[{"name":"billingAccounts/ABCDEF-123456-ABCDEF","open":true},{"name":"billingAccounts/123456-ABCDEF-123456","open":true}]`
	_, err := proposeProjectCreation(context.Background(), r, io.Discard, onboardingAnswers("yes", "2", "Test deployment", "yes"), func(account string) error {
		if account != "123456-ABCDEF-123456" {
			t.Fatal(account)
		}
		r.audited = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
