package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// proposeProjectCreation creates only an explicitly approved project. The audit
// callback must verify the selected account before any cloud mutation occurs.
func proposeProjectCreation(ctx context.Context, runner Runner, out io.Writer, ask func(string) (string, error), audit func(string) error) (string, error) {
	fmt.Fprintln(out, "No accessible projects were found. You can create a project for this deployment.")
	answer, err := ask("Create a new Google Cloud project? Enter yes, or Enter to cancel: ")
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		return "", nil
	}
	run := func(args ...string) CommandResult {
		callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		return runner.Run(callCtx, Command{Name: "gcloud", Args: args})
	}
	accountsResult := run("billing", "accounts", "list", "--filter=open:true", "--format=json(name,displayName,open)")
	var accounts []struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Open        bool   `json:"open"`
	}
	if accountsResult.ExitCode != 0 || json.Unmarshal([]byte(accountsResult.Stdout), &accounts) != nil || strings.TrimSpace(accountsResult.Stdout) == "null" {
		return "", fmt.Errorf("could not list billing accounts; check billing account permissions; no project was created")
	}
	if len(accounts) == 0 {
		return "", fmt.Errorf("create or obtain access to an open billing account first: https://console.cloud.google.com/billing; no project was created")
	}
	pattern := regexp.MustCompile(`^billingAccounts/[A-Fa-f0-9]{6}-[A-Fa-f0-9]{6}-[A-Fa-f0-9]{6}$`)
	for i, account := range accounts {
		if !account.Open || !pattern.MatchString(account.Name) {
			return "", fmt.Errorf("billing account list contained invalid or closed account information; no project was created")
		}
		fmt.Fprintf(out, "%d. %s (%s)\n", i+1, safeProjectLabel(account.DisplayName), account.Name)
	}
	index := 0
	if len(accounts) > 1 {
		selection, e := ask("Billing account number [1]: ")
		if e != nil {
			return "", e
		}
		if selection != "" {
			n, e := strconv.Atoi(selection)
			if e != nil || n < 1 || n > len(accounts) {
				return "", fmt.Errorf("select a billing account number from the list")
			}
			index = n - 1
		}
	}
	name, err := ask("Project display name [My deployment]: ")
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "My deployment"
	}
	if len(name) < 4 || len(name) > 30 || !regexp.MustCompile(`^[A-Za-z0-9 '"!-]+$`).MatchString(name) {
		return "", fmt.Errorf("project display name must be 4–30 characters using letters, digits, spaces, hyphens, apostrophes, double quotes or exclamation marks")
	}
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("could not generate a project ID")
	}
	project := "gcp-deploy-" + hex.EncodeToString(random[:])
	account := strings.TrimPrefix(accounts[index].Name, "billingAccounts/")
	fmt.Fprintf(out, "Create project: %s (%s)\nLink billing account: %s\nBilling will be enabled. This does not guarantee zero charges.\n", project, name, account)
	answer, err = ask("Enter yes to create this project and link this billing account: ")
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		return "", nil
	}
	if audit == nil {
		return "", fmt.Errorf("billing account usage check is unavailable; no project was created")
	}
	if err := audit(account); err != nil {
		return "", err
	}
	result := run("projects", "create", project, "--name="+name, "--format=json", "--quiet")
	var created struct {
		ProjectID string `json:"projectId"`
	}
	if result.ExitCode != 0 || json.Unmarshal([]byte(result.Stdout), &created) != nil || created.ProjectID != project {
		return "", fmt.Errorf("could not confirm creation of project %s; inspect https://console.cloud.google.com/home/dashboard?project=%s before retrying; no billing link was attempted", project, project)
	}
	result = run("billing", "projects", "link", project, "--billing-account="+account, "--format=json", "--quiet")
	var linked struct {
		Name               string `json:"name"`
		BillingAccountName string `json:"billingAccountName"`
		BillingEnabled     bool   `json:"billingEnabled"`
	}
	if result.ExitCode != 0 || json.Unmarshal([]byte(result.Stdout), &linked) != nil || linked.Name != "projects/"+project+"/billingInfo" || linked.BillingAccountName != "billingAccounts/"+account || !linked.BillingEnabled {
		return "", fmt.Errorf("project %s was created, but its billing link could not be confirmed; the project was preserved. Check https://console.cloud.google.com/billing/linkedaccount?project=%s and rerun setup", project, project)
	}
	fmt.Fprintf(out, "Project created and billing linked: %s\n", project)
	return project, nil
}

func safeProjectLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
