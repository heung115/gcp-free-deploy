package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

const monitoringAPI = "monitoring.googleapis.com"

type deploymentEmailChannel struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Labels  map[string]string `json:"labels"`
}

func (c deploymentEmailChannel) valid(project, number, email string) bool {
	prefix := "projects/" + project + "/notificationChannels/"
	numbered := "projects/" + number + "/notificationChannels/"
	name := strings.TrimPrefix(c.Name, prefix)
	if name == c.Name {
		name = strings.TrimPrefix(c.Name, numbered)
	}
	return regexp.MustCompile(`^[0-9]+$`).MatchString(name) && c.Type == "email" && c.Enabled && strings.EqualFold(c.Labels["email_address"], email)
}

// configureDeploymentBudget discovers the deployment owner's email and linked billing
// account. It preserves existing managed budget amounts and never edits custom alerts.
func configureDeploymentBudget(ctx context.Context, out io.Writer, runner Runner, project string) error {
	if !projectIDPattern.MatchString(project) {
		return fmt.Errorf("automatic cost alerts require a valid project ID")
	}
	if err := requireTool(runner, "gcloud"); err != nil {
		return err
	}
	run := func(operation string, args ...string) (string, error) {
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		result := runner.Run(callCtx, Command{Name: "gcloud", Args: append(args, "--project="+project, "--quiet")})
		if result.ExitCode != 0 {
			return "", fmt.Errorf("automatic cost alerts: %s failed; verify gcloud login, project Monitoring/service enablement permissions, and Billing Account Costs Manager access", operation)
		}
		return result.Stdout, nil
	}
	raw, err := run("login lookup", "config", "get-value", "account")
	if err != nil {
		return err
	}
	email := strings.TrimSpace(raw)
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || strings.HasSuffix(strings.ToLower(email), ".gserviceaccount.com") {
		return fmt.Errorf("automatic cost alerts need a signed-in user email; service accounts cannot receive email (configure a budget explicitly for unattended deployments)")
	}
	raw, err = run("project lookup", "projects", "describe", project, "--format=json(projectNumber)")
	if err != nil {
		return err
	}
	var identity struct {
		ProjectNumber string `json:"projectNumber"`
	}
	if json.Unmarshal([]byte(raw), &identity) != nil || !regexp.MustCompile(`^[0-9]+$`).MatchString(identity.ProjectNumber) {
		return fmt.Errorf("automatic cost alerts: invalid project number")
	}
	raw, err = run("billing account lookup", "billing", "projects", "describe", project, "--format=json(billingAccountName,billingEnabled)")
	if err != nil {
		return err
	}
	var link struct {
		BillingAccountName string `json:"billingAccountName"`
		BillingEnabled     bool   `json:"billingEnabled"`
	}
	if json.Unmarshal([]byte(raw), &link) != nil || !link.BillingEnabled || !regexp.MustCompile(`^billingAccounts/[A-Fa-f0-9]{6}-[A-Fa-f0-9]{6}-[A-Fa-f0-9]{6}$`).MatchString(link.BillingAccountName) {
		return fmt.Errorf("automatic cost alerts: project has no verified active billing account")
	}
	account := strings.TrimPrefix(link.BillingAccountName, "billingAccounts/")
	for _, api := range []string{budgetAPI, monitoringAPI} {
		raw, err = run("API lookup", "services", "list", "--enabled", "--filter=config.name="+api, "--format=value(config.name)")
		if err != nil {
			return err
		}
		if strings.TrimSpace(raw) == "" {
			if _, err = run("API enablement", "services", "enable", api); err != nil {
				return err
			}
		} else if strings.TrimSpace(raw) != api {
			return fmt.Errorf("automatic cost alerts: unexpected API status")
		}
	}
	raw, err = run("existing budget lookup", "billing", "budgets", "list", "--billing-account="+account, "--billing-project="+project, "--format=json")
	if err != nil {
		return err
	}
	var budgets []billingBudget
	if strings.TrimSpace(raw) == "null" || json.Unmarshal([]byte(raw), &budgets) != nil {
		return fmt.Errorf("automatic cost alerts: invalid budget listing")
	}
	amount, channel := "1", ""
	found := false
	for _, b := range budgets {
		if b.DisplayName != "gcp-free-deploy-"+project+"-cost-alert" {
			continue
		}
		if found {
			return fmt.Errorf("automatic cost alerts: duplicate managed budgets; preserved unchanged")
		}
		found = true
		money := b.Amount.SpecifiedAmount
		units := money.Units
		if units == "" {
			units = "0"
		}
		amount = fmt.Sprintf("%s.%09d", units, money.Nanos)
		n, amountErr := budgetAmountNanos(amount)
		channels := b.NotificationsRule.MonitoringNotificationChannels
		if len(channels) == 1 {
			channel = channels[0]
		}
		if amountErr != nil || channel == "" || !b.matches(identity.ProjectNumber, n, channel) {
			return fmt.Errorf("automatic cost alerts: existing budget has custom settings or no direct email channel; preserved unchanged, inspect it using the budget command")
		}
	}
	raw, err = run("email channel lookup", "beta", "monitoring", "channels", "list", "--format=json")
	if err != nil {
		return err
	}
	var channels []deploymentEmailChannel
	if strings.TrimSpace(raw) == "null" || json.Unmarshal([]byte(raw), &channels) != nil {
		return fmt.Errorf("automatic cost alerts: invalid email channel listing")
	}
	if found {
		for _, c := range channels {
			if c.Name == channel && c.valid(project, identity.ProjectNumber, email) {
				return configureBudget(ctx, []string{"--billing-account=" + account, "--project=" + project, "--amount=" + amount, "--notification-channel=" + channel, "--enable-api"}, out, out, runner)
			}
		}
		return fmt.Errorf("automatic cost alerts: existing budget email channel is disabled, missing, or belongs to another user; preserved unchanged")
	}
	for _, c := range channels {
		if c.valid(project, identity.ProjectNumber, email) {
			channel = c.Name
			break
		}
	}
	if channel == "" {
		body, _ := json.Marshal(map[string]any{"type": "email", "displayName": "gcp-free-deploy cost alerts", "enabled": true, "labels": map[string]string{"email_address": email}})
		raw, err = run("email channel creation", "beta", "monitoring", "channels", "create", "--channel-content="+string(body), "--format=json")
		if err != nil {
			return err
		}
		var c deploymentEmailChannel
		if json.Unmarshal([]byte(raw), &c) != nil || !c.valid(project, identity.ProjectNumber, email) {
			return fmt.Errorf("automatic cost alerts: unexpected created email channel; inspect Monitoring before retrying")
		}
		channel = c.Name
	}
	fmt.Fprintln(out, "Configuring cost alert emails for your signed-in account automatically.")
	return configureBudget(ctx, []string{"--billing-account=" + account, "--project=" + project, "--amount=" + amount, "--notification-channel=" + channel, "--enable-api"}, out, out, runner)
}
