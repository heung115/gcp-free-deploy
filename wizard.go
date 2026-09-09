package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runWizard gathers safe defaults, then hands off to the existing deployment
// workflow. It never bypasses Terraform's plan or deletion confirmation.
func runWizard(ctx context.Context, in io.Reader, out, errOut io.Writer, runner Runner, workdir string) error {
	reader := bufio.NewReader(in)
	ask := func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		line, err := reader.ReadString('\n')
		// EOF is cancellation even when a partial answer was supplied.
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	err := wizardFlow(ctx, reader, out, errOut, runner, workdir, ask)
	if errors.Is(err, io.EOF) {
		fmt.Fprintln(out, "\nSetup cancelled. No further actions will be taken.")
		return nil
	}
	return err
}

func wizardFlow(ctx context.Context, reader *bufio.Reader, out, errOut io.Writer, runner Runner, workdir string, ask func(string) (string, error)) error {
	fmt.Fprintln(out, "gcp-free-deploy guided setup\n1. Deploy a new app\n2. Check or update the deployment in this folder\n3. Delete the deployment in this folder")
	choice, err := ask("Choose [1]: ")
	if err != nil {
		return err
	}
	workdir, err = filepath.Abs(workdir)
	if err != nil {
		return err
	}
	switch choice {
	case "2":
		return withWorkdirLock(workdir, func() error {
			return deployTerraform(ctx, reader, out, runner, workdir, upOptions{ConfigPath: filepath.Join(workdir, "gcp-free-deploy.json"), StartupTimeout: defaultStartupTimeout})
		})
	case "3":
		return withWorkdirLock(workdir, func() error { return destroyTerraform(ctx, reader, out, runner, workdir, downOptions{}) })
	case "", "1":
	default:
		return fmt.Errorf("choose 1, 2, or 3")
	}
	for _, tool := range []struct{ name, url string }{
		{"terraform", "https://developer.hashicorp.com/terraform/install"},
		{"gcloud", "https://cloud.google.com/sdk/docs/install"},
		{"curl", "https://curl.se/download.html"},
	} {
		if runner.LookPath(tool.name) != nil {
			return fmt.Errorf("install %s, then run start again: %s", tool.name, tool.url)
		}
	}
	run := func(args ...string) CommandResult {
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		return runner.Run(callCtx, Command{Name: "gcloud", Args: args, Dir: workdir})
	}
	// Never expose credential command output, including its error diagnostics.
	authenticated := func() bool {
		auth := run("auth", "list", "--filter=status:ACTIVE", "--format=value(account)")
		if auth.ExitCode != 0 || strings.TrimSpace(auth.Stdout) == "" {
			return false
		}
		adc := run("auth", "application-default", "print-access-token")
		return adc.ExitCode == 0 && strings.TrimSpace(adc.Stdout) != ""
	}
	if !authenticated() {
		interactive, ok := runner.(InteractiveRunner)
		if !ok {
			return fmt.Errorf("connect your Google account and Terraform authentication: gcloud auth login --update-adc; then run start again")
		}
		answer, err := ask("Google authentication is needed. Enter yes to open browser login and configure Terraform authentication: ")
		if err != nil {
			return err
		}
		if !strings.EqualFold(answer, "yes") {
			fmt.Fprintln(out, "Setup cancelled.")
			return nil
		}
		login := interactive.RunInteractive(ctx, Command{Name: "gcloud", Args: []string{"auth", "login", "--update-adc"}, Dir: workdir}, reader, out, errOut)
		if login.ExitCode != 0 || !authenticated() {
			return fmt.Errorf("Google authentication was not completed; run gcloud auth login --update-adc, then run start again")
		}
	}
	fmt.Fprintln(out, "Google account and Terraform authentication: ready.")
	result := run("projects", "list", "--filter=lifecycleState:ACTIVE", "--format=json(projectId,name)")
	var projects []struct {
		ProjectID string `json:"projectId"`
		Name      string `json:"name"`
	}
	if result.ExitCode != 0 || json.Unmarshal([]byte(result.Stdout), &projects) != nil {
		return fmt.Errorf("could not list projects; check your Google account and project permissions")
	}
	if len(projects) == 0 {
		return fmt.Errorf("create a Google Cloud project and connect a billing account first: https://console.cloud.google.com/projectcreate")
	}
	for i, p := range projects {
		if !projectIDPattern.MatchString(p.ProjectID) {
			return fmt.Errorf("project list contained an invalid project ID")
		}
		fmt.Fprintf(out, "%d. %s\n", i+1, p.ProjectID)
	}
	selection, err := ask("Project number [1]: ")
	if err != nil {
		return err
	}
	if selection == "" {
		selection = "1"
	}
	index, err := strconv.Atoi(selection)
	if err != nil || index < 1 || index > len(projects) {
		return fmt.Errorf("select a project number from the list")
	}
	project := projects[index-1].ProjectID
	billing := run("billing", "projects", "describe", project, "--format=json(billingAccountName,billingEnabled)")
	var link struct {
		BillingAccountName string `json:"billingAccountName"`
		BillingEnabled     bool   `json:"billingEnabled"`
	}
	if billing.ExitCode != 0 || json.Unmarshal([]byte(billing.Stdout), &link) != nil || !link.BillingEnabled || !strings.HasPrefix(link.BillingAccountName, "billingAccounts/") || len(link.BillingAccountName) <= len("billingAccounts/") {
		return fmt.Errorf("project billing could not be verified; connect a billing account or check permissions at https://console.cloud.google.com/billing/linkedaccount?project=%s; no changes made", project)
	}
	cfg := DeployConfig{ProjectID: project, Zone: "us-central1-a", MachineType: "e2-micro", DiskSizeGB: 10, ContainerPort: 80, MaxRuntimeHours: 24}
	source, err := ask("Docker image with a fixed tag, or public GitHub repository URL: ")
	if err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(source), "https://") {
		cfg.Source, cfg.GithubRepo = "github", source
		fmt.Fprintln(out, "The public repository must contain a Dockerfile at its root.")
	} else {
		cfg.Source, cfg.DockerImage = "docker", source
	}
	port, err := ask("Container port [80]: ")
	if err != nil {
		return err
	}
	if port != "" {
		cfg.ContainerPort, err = strconv.Atoi(port)
		if err != nil {
			return fmt.Errorf("container port must be a number from 1 to 65535")
		}
	}
	runtime, err := ask("Run mode: 1 = stop after 24 hours, 2 = continuous [1]: ")
	if err != nil {
		return err
	}
	switch runtime {
	case "", "1":
	case "2":
		cfg.MaxRuntimeHours = 0
	default:
		return fmt.Errorf("choose run mode 1 or 2")
	}
	fmt.Fprintln(out, "Checking this computer's public IPv4 using https://api.ipify.org ...")
	ipCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ipResult := runner.Run(ipCtx, Command{Name: "curl", Args: []string{"-4", "--fail", "--silent", "--show-error", "--max-time", "10", "https://api.ipify.org"}, Dir: workdir})
	cancel()
	detected := strings.TrimSpace(ipResult.Stdout)
	allowed := ""
	if ipResult.ExitCode == 0 && wizardPublicIPv4(detected) {
		answer, err := ask(fmt.Sprintf("Allow HTTP access only from %s? Enter yes, or enter another public IPv4: ", detected))
		if err != nil {
			return err
		}
		if strings.EqualFold(answer, "yes") {
			allowed = detected
		} else {
			allowed = answer
		}
	} else {
		fmt.Fprintln(errOut, "Could not detect a public IPv4 automatically.")
		allowed, err = ask("Public IPv4 allowed to access the app: ")
		if err != nil {
			return err
		}
	}
	if !wizardPublicIPv4(allowed) {
		return fmt.Errorf("enter one public IPv4 address; broad ranges and private addresses are not accepted by guided setup")
	}
	cfg.AllowedSourceRanges = []string{net.ParseIP(allowed).To4().String() + "/32"}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := guardCostProfile(cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nProject: %s\nServer: e2-micro, us-central1-a, 10 GB standard disk, Standard network\nHTTP access: %s\n", project, cfg.AllowedSourceRanges[0])
	if cfg.MaxRuntimeHours == 0 {
		fmt.Fprintln(out, "Run time: continuous.")
	} else {
		fmt.Fprintln(out, "Run time: stops after 24 hours; the disk remains until you delete the deployment.")
	}
	fmt.Fprintln(out, "Cost email alerts: automatic. Free allowances are shared; alerts are delayed and do not cap spending.")
	fmt.Fprintln(out, "Continuing creates a deployment folder and enables the Compute API if needed. You will review and approve the Terraform plan separately before server creation.")
	answer, err := ask("Enter yes to prepare this deployment: ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(answer, "yes") {
		fmt.Fprintln(out, "Setup cancelled; no changes made.")
		return nil
	}
	api := run("services", "list", "--enabled", "--project="+project, "--filter=config.name=compute.googleapis.com", "--format=value(config.name)")
	if api.ExitCode != 0 {
		return fmt.Errorf("could not check Compute API status; check project permissions")
	}
	status := strings.TrimSpace(api.Stdout)
	if status != "" && status != "compute.googleapis.com" {
		return fmt.Errorf("unexpected Compute API status; no changes made")
	}
	dir, err := os.MkdirTemp(workdir, "gcp-deploy-")
	if err != nil {
		return fmt.Errorf("create deployment folder: %w", err)
	}
	configPath := filepath.Join(dir, "gcp-free-deploy.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, append(data, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "Deployment folder: %s\nKeep this folder to manage or delete the server. To resume: cd into this folder and run gcp-free-deploy up.\n", dir)
	if status == "" {
		enabled := run("services", "enable", "compute.googleapis.com", "--project="+project, "--quiet")
		if enabled.ExitCode != 0 {
			return fmt.Errorf("could not enable Compute API; check Service Usage permissions; your configuration is saved in %s", dir)
		}
	}
	return withWorkdirLock(dir, func() error {
		return deployTerraform(ctx, reader, out, runner, dir, upOptions{ConfigPath: configPath, StartupTimeout: defaultStartupTimeout})
	})
}

func wizardPublicIPv4(value string) bool {
	ip := net.ParseIP(value)
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	// Exclude shared, documentation, benchmarking and reserved address blocks too.
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		_, subnet, _ := net.ParseCIDR(block)
		if subnet.Contains(ip) {
			return false
		}
	}
	return true
}
