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
	workdir, err := filepath.Abs(workdir)
	if err != nil {
		return err
	}
	known, err := discoverDeployments(workdir)
	if err != nil {
		return fmt.Errorf("read saved deployments: %w", err)
	}
	fmt.Fprintln(out, "gcp-free-deploy guided setup")
	if len(known) > 0 {
		fmt.Fprintln(out, "0. Deploy a new app")
		for i, d := range known {
			fmt.Fprintf(out, "%d. %s — %s (%s)\n", i+1, d.ProjectID, d.Source, d.Dir)
		}
		choice, err := ask("Choose a deployment, or 0 for new [0]: ")
		if err != nil {
			return err
		}
		if choice != "" && choice != "0" {
			n, e := strconv.Atoi(choice)
			if e != nil || n < 1 || n > len(known) {
				return fmt.Errorf("select a deployment from the list")
			}
			dir := known[n-1].Dir
			action, e := ask("1. Check or update   2. Delete [1]: ")
			if e != nil {
				return e
			}
			return withWorkdirLock(dir, func() error {
				switch action {
				case "", "1":
					return deployTerraform(ctx, reader, out, runner, dir, upOptions{ConfigPath: filepath.Join(dir, "gcp-free-deploy.json"), StartupTimeout: defaultStartupTimeout})
				case "2":
					return destroyTerraform(ctx, reader, out, runner, dir, downOptions{})
				default:
					return fmt.Errorf("choose 1 or 2")
				}
			})
		}
	}
	source, err := ask("Docker image with a fixed tag, or public GitHub repository URL: ")
	if err != nil {
		return err
	}
	for _, tool := range []struct{ name, url string }{
		{"terraform", "https://developer.hashicorp.com/terraform/install"},
		{"gcloud", "https://cloud.google.com/sdk/docs/install"},
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
	index := 1
	if len(projects) > 1 {
		selection, e := ask("Project number [1]: ")
		if e != nil {
			return e
		}
		if selection != "" {
			index, e = strconv.Atoi(selection)
			if e != nil || index < 1 || index > len(projects) {
				return fmt.Errorf("select a project number from the list")
			}
		}
	} else {
		fmt.Fprintf(out, "Using the only available project: %s\n", projects[0].ProjectID)
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
	if strings.HasPrefix(strings.ToLower(source), "https://") {
		cfg.Source, cfg.GithubRepo = "github", source
		fmt.Fprintln(out, "The public repository must contain a Dockerfile at its root.")
	} else {
		cfg.Source, cfg.DockerImage = "docker", source
	}
	fmt.Fprintln(out, "Checking this computer's public IPv4 using https://api.ipify.org ...")
	ipCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	detected, ipErr := runnerHTTPGet(ipCtx, runner, "https://api.ipify.org")
	cancel()
	allowed := strings.TrimSpace(detected)
	if ipErr != nil || !wizardPublicIPv4(allowed) {
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
	for {
		cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := guardCostProfile(cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nProject: %s\nApp: %s\nPort: %d\nServer: e2-micro, us-central1-a, 10 GB standard disk, Standard network\nHTTP access: %s only (no HTTPS)\n", project, source, cfg.ContainerPort, cfg.AllowedSourceRanges[0])
		if cfg.MaxRuntimeHours == 0 {
			fmt.Fprintln(out, "Run time: continuous.")
		} else {
			fmt.Fprintln(out, "Run time: stop after 24 hours; disk remains until deletion.")
		}
		fmt.Fprintln(out, "Cost email alerts: automatic. Free allowances are shared; alerts do not cap spending.")
		fmt.Fprintln(out, "Continue prepares a local folder and enables the Compute API if needed. Server creation still requires approval of the Terraform plan.")
		answer, e := ask("Enter = continue, port/runtime/ip = change a setting, cancel = exit: ")
		if e != nil {
			return e
		}
		switch strings.ToLower(answer) {
		case "":
			goto prepare
		case "cancel":
			fmt.Fprintln(out, "Setup cancelled; no deployment resources created.")
			return nil
		case "port":
			v, e := ask("Container port: ")
			if e != nil {
				return e
			}
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > 65535 {
				fmt.Fprintln(out, "Enter a port from 1 to 65535.")
				continue
			}
			cfg.ContainerPort = n
		case "runtime":
			v, e := ask("1 = stop after 24 hours, 2 = continuous: ")
			if e != nil {
				return e
			}
			if v == "1" {
				cfg.MaxRuntimeHours = 24
			} else if v == "2" {
				cfg.MaxRuntimeHours = 0
			} else {
				fmt.Fprintln(out, "Choose 1 or 2.")
			}
		case "ip":
			v, e := ask("Public IPv4 allowed to access the app: ")
			if e != nil {
				return e
			}
			if !wizardPublicIPv4(v) {
				fmt.Fprintln(out, "Enter one public IPv4 address.")
				continue
			}
			cfg.AllowedSourceRanges = []string{net.ParseIP(v).To4().String() + "/32"}
		default:
			fmt.Fprintln(out, "Press Enter to continue, or choose port, runtime, ip, or cancel.")
		}
	}
prepare:
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
	if err := rememberDeployment(dir); err != nil {
		fmt.Fprintf(errOut, "Could not save deployment shortcut: %v. Keep the folder path below.\n", err)
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
