package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const budgetAPI = "billingbudgets.googleapis.com"

// budgetAmountNanos keeps Money comparisons exact and bounds amounts to avoid overflow.
func budgetAmountNanos(s string) (int64, error) {
	if !regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,9})?$`).MatchString(s) {
		return 0, fmt.Errorf("amount must be a positive decimal with at most 9 integer and 9 fractional digits, without a currency suffix")
	}
	parts := strings.SplitN(s, ".", 2)
	units, _ := strconv.ParseInt(parts[0], 10, 64)
	var nanos int64
	if len(parts) == 2 {
		nanos, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 9-len(parts[1])), 10, 64)
	}
	value := units*1_000_000_000 + nanos
	if value <= 0 {
		return 0, fmt.Errorf("amount must be greater than zero")
	}
	return value, nil
}

type billingBudget struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Amount      struct {
		SpecifiedAmount struct {
			CurrencyCode string `json:"currencyCode"`
			Units        string `json:"units"`
			Nanos        int64  `json:"nanos"`
		} `json:"specifiedAmount"`
		LastPeriodAmount json.RawMessage `json:"lastPeriodAmount"`
	} `json:"amount"`
	BudgetFilter   map[string]json.RawMessage `json:"budgetFilter"`
	ThresholdRules []struct {
		ThresholdPercent float64 `json:"thresholdPercent"`
		SpendBasis       string  `json:"spendBasis"`
	} `json:"thresholdRules"`
	NotificationsRule struct {
		DisableDefaultIAMRecipients    bool     `json:"disableDefaultIamRecipients"`
		PubsubTopic                    string   `json:"pubsubTopic"`
		MonitoringNotificationChannels []string `json:"monitoringNotificationChannels"`
	} `json:"notificationsRule"`
}

func (b billingBudget) matches(projectNumber string, amount int64, channel string) bool {
	money := b.Amount.SpecifiedAmount
	unitText := money.Units
	if unitText == "" {
		unitText = "0"
	}
	units, err := strconv.ParseInt(unitText, 10, 64)
	if err != nil || units < 0 || units > 999999999 || money.Nanos < 0 || money.Nanos >= 1_000_000_000 || money.CurrencyCode == "" || units*1_000_000_000+money.Nanos != amount || (len(b.Amount.LastPeriodAmount) > 0 && string(b.Amount.LastPeriodAmount) != "null") {
		return false
	}
	var projects []string
	var period, credits string
	if json.Unmarshal(b.BudgetFilter["projects"], &projects) != nil || len(projects) != 1 || projects[0] != "projects/"+projectNumber || json.Unmarshal(b.BudgetFilter["calendarPeriod"], &period) != nil || period != "MONTH" || json.Unmarshal(b.BudgetFilter["creditTypesTreatment"], &credits) != nil || credits != "INCLUDE_ALL_CREDITS" {
		return false
	}
	for key, value := range b.BudgetFilter {
		if key == "projects" || key == "calendarPeriod" || key == "creditTypesTreatment" {
			continue
		}
		s := strings.TrimSpace(string(value))
		if s != "[]" && s != "{}" && s != "null" && s != `""` {
			return false
		}
	}
	if b.NotificationsRule.DisableDefaultIAMRecipients || b.NotificationsRule.PubsubTopic != "" || len(b.ThresholdRules) != 3 {
		return false
	}
	channels := b.NotificationsRule.MonitoringNotificationChannels
	if channel == "" && len(channels) != 0 {
		return false
	}
	if channel != "" && (len(channels) != 1 || channels[0] != channel) {
		return false
	}
	seen := map[float64]bool{}
	for _, rule := range b.ThresholdRules {
		if (rule.SpendBasis != "CURRENT_SPEND" && rule.SpendBasis != "") || (rule.ThresholdPercent != .01 && rule.ThresholdPercent != .1 && rule.ThresholdPercent != 1) || seen[rule.ThresholdPercent] {
			return false
		}
		seen[rule.ThresholdPercent] = true
	}
	return true
}

// configureBudget only creates the named budget; existing budgets are never updated.
func configureBudget(ctx context.Context, args []string, out, errOut io.Writer, runner Runner) error {
	set := flag.NewFlagSet("budget", flag.ContinueOnError)
	set.SetOutput(errOut)
	account := set.String("billing-account", "", "required billing account ID (XXXXXX-XXXXXX-XXXXXX)")
	project := set.String("project", "", "required project ID to monitor")
	amount := set.String("amount", "1", "monthly amount in the billing account currency; alerts at 1%, 10%, and 100%")
	channel := set.String("notification-channel", "", "optional existing Cloud Monitoring email channel resource name; keeps default billing recipients enabled")
	dryRun := set.Bool("dry-run", false, "read existing settings and show the proposal without changing cloud resources")
	enableAPI := set.Bool("enable-api", false, "allow enabling the Cloud Billing Budget API in the target project if needed")
	set.Usage = func() {
		fmt.Fprintln(errOut, "Usage: gcp-free-deploy budget --billing-account ACCOUNT --project PROJECT [--amount 1] [--dry-run] [--enable-api]")
		fmt.Fprintln(errOut, "Create a monthly email cost alert for billing account administrators and users. Existing budgets are preserved.")
		fmt.Fprintln(errOut, "Billing data and emails are delayed. This is an alert, not a spending cap or automatic shutdown.")
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 || !projectIDPattern.MatchString(*project) || !regexp.MustCompile(`^[A-Fa-f0-9]{6}-[A-Fa-f0-9]{6}-[A-Fa-f0-9]{6}$`).MatchString(*account) {
		return fmt.Errorf("budget requires explicit valid --billing-account and --project; positional arguments are not supported")
	}
	if *channel != "" && !regexp.MustCompile(`^projects/[a-z0-9][a-z0-9-]*/notificationChannels/[0-9]+$`).MatchString(*channel) {
		return fmt.Errorf("notification-channel must be projects/PROJECT/notificationChannels/NUMERIC_ID")
	}
	amountNanos, err := budgetAmountNanos(*amount)
	if err != nil {
		return err
	}
	if err := requireTool(runner, "gcloud"); err != nil {
		return err
	}
	run := func(operation string, args ...string) (string, error) {
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		// Only Budget API calls need the explicitly enabled target quota project.
		// Applying it to billing projects describe would require an unrelated API there.
		if len(args) >= 2 && args[0] == "billing" && args[1] == "budgets" {
			args = append(args, "--billing-project="+*project)
		}
		result := runner.Run(callCtx, Command{Name: "gcloud", Args: append(args, "--project="+*project, "--quiet")})
		if result.ExitCode != 0 {
			return "", fmt.Errorf("budget %s failed; verify gcloud authentication, project permissions, Billing Account Costs Manager (or administrator) access, and Cloud Billing Budget API availability", operation)
		}
		return result.Stdout, nil
	}
	projectJSON, err := run("project lookup", "projects", "describe", *project, "--format=json(projectNumber)")
	if err != nil {
		return err
	}
	var identity struct {
		ProjectNumber string `json:"projectNumber"`
	}
	if json.Unmarshal([]byte(projectJSON), &identity) != nil || !regexp.MustCompile(`^[0-9]+$`).MatchString(identity.ProjectNumber) {
		return fmt.Errorf("budget project lookup returned an invalid project number")
	}
	linkJSON, err := run("billing account verification", "billing", "projects", "describe", *project, "--format=json(billingAccountName,billingEnabled)")
	if err != nil {
		return err
	}
	var link struct {
		BillingAccountName string `json:"billingAccountName"`
		BillingEnabled     bool   `json:"billingEnabled"`
	}
	if json.Unmarshal([]byte(linkJSON), &link) != nil || !link.BillingEnabled || !strings.EqualFold(link.BillingAccountName, "billingAccounts/"+*account) {
		return fmt.Errorf("project is not verified as billing-enabled on the supplied billing account; no changes made")
	}
	name := "gcp-free-deploy-" + *project + "-cost-alert"
	fmt.Fprintf(out, "Monthly email budget: %s; amount %s in your billing account currency (not necessarily USD).\n", name, *amount)
	fmt.Fprintln(out, "Actual spend alerts: 1%, 10%, 100%, after all credits; default Billing Account Administrator/User email recipients enabled.")
	displayAmount := float64(amountNanos) / 1_000_000_000
	fmt.Fprintf(out, "Alert thresholds: %g, %g, and %g billing currency units. Billing reporting and emails can be delayed; this does not cap spending or stop servers.\n", displayAmount*.01, displayAmount*.1, displayAmount)
	api, err := run("API status lookup", "services", "list", "--enabled", "--filter=config.name="+budgetAPI, "--format=value(config.name)")
	if err != nil {
		return err
	}
	if strings.TrimSpace(api) != budgetAPI {
		if strings.TrimSpace(api) != "" {
			return fmt.Errorf("unexpected Budget API status; no changes made")
		}
		if *dryRun {
			fmt.Fprintln(out, "Dry run: Budget API is disabled. Re-run with --enable-api to enable it and inspect/create the budget; existing budgets could not be checked.")
			return nil
		}
		if !*enableAPI {
			return fmt.Errorf("Cloud Billing Budget API is disabled in this project; re-run with --enable-api to authorize enabling it")
		}
		if _, err := run("API enablement", "services", "enable", budgetAPI); err != nil {
			return err
		}
	}
	existingJSON, err := run("existing budget lookup", "billing", "budgets", "list", "--billing-account="+*account, "--format=json")
	if err != nil {
		return err
	}
	var existing []billingBudget
	if strings.TrimSpace(existingJSON) == "null" || json.Unmarshal([]byte(existingJSON), &existing) != nil {
		return fmt.Errorf("existing budget lookup returned invalid JSON; no budget created")
	}
	var matches []billingBudget
	for _, budget := range existing {
		if budget.DisplayName == name {
			matches = append(matches, budget)
		}
	}
	if len(matches) > 1 {
		return fmt.Errorf("multiple budgets use managed name %q; inspect them manually, no budgets changed", name)
	}
	if len(matches) == 1 {
		if !matches[0].matches(identity.ProjectNumber, amountNanos, *channel) {
			return fmt.Errorf("existing budget %q has different or custom settings; preserved unchanged, inspect it in Cloud Billing before continuing", name)
		}
		fmt.Fprintf(out, "Existing matching budget verified (%s). No changes needed.\n", matches[0].Amount.SpecifiedAmount.CurrencyCode)
		return nil
	}
	if *dryRun {
		fmt.Fprintln(out, "Dry run: would create this budget. No cloud resources changed.")
		return nil
	}
	createArgs := []string{"billing", "budgets", "create", "--billing-account=" + *account, "--display-name=" + name, "--budget-amount=" + *amount, "--calendar-period=month", "--filter-projects=projects/" + identity.ProjectNumber, "--credit-types-treatment=include-all-credits", "--threshold-rule=percent=0.01,basis=current-spend", "--threshold-rule=percent=0.1,basis=current-spend", "--threshold-rule=percent=1,basis=current-spend", "--format=json"}
	if *channel != "" {
		createArgs = append(createArgs, "--notifications-rule-monitoring-notification-channels="+*channel)
	}
	createdJSON, err := run("creation", createArgs...)
	if err != nil {
		return err
	}
	var created billingBudget
	if json.Unmarshal([]byte(createdJSON), &created) != nil || created.DisplayName != name || !created.matches(identity.ProjectNumber, amountNanos, *channel) {
		return fmt.Errorf("budget creation returned unexpected settings; inspect Cloud Billing before retrying (a budget may already exist)")
	}
	fmt.Fprintf(out, "Created and verified monthly budget (%s). Email delivery is handled by Cloud Billing; this does not send a test email.\n", created.Amount.SpecifiedAmount.CurrencyCode)
	return nil
}
