package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultConfigPath = "gcp-free-deploy.json"
	applyPlanName     = ".gcp-free-deploy.tfplan"
	destroyPlanName   = ".gcp-free-deploy-destroy.tfplan"
)

var computeFreeTierRegions = map[string]bool{
	"us-central1": true,
	"us-east1":    true,
	"us-west1":    true,
}

type upOptions struct {
	ConfigPath      string
	AutoApprove     bool
	PlanOnly        bool
	AllowPublicHTTP bool
	StartupTimeout  time.Duration
}

type downOptions struct {
	AutoApprove bool
}

func runCLI(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, runner Runner, workdir string) error {
	if len(args) == 0 {
		printUsage(out)
		return nil
	}

	switch args[0] {
	case "init":
		if err := parseInitOptions(args[1:], errOut); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		return withWorkdirLock(workdir, func() error {
			if err := ensureRuntimeAssets(workdir); err != nil {
				return err
			}
			if err := verifyManagedTerraformFiles(workdir); err != nil {
				return err
			}
			fmt.Fprintln(out, "Prepared runtime files: main.tf, provider lock, and example config")
			return nil
		})
	case "up":
		opts, err := parseUpOptions(args[1:], errOut)
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		return withWorkdirLock(workdir, func() error {
			return deployTerraform(ctx, in, out, runner, workdir, opts)
		})
	case "down":
		opts, err := parseDownOptions(args[1:], errOut)
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		return withWorkdirLock(workdir, func() error {
			return destroyTerraform(ctx, in, out, runner, workdir, opts)
		})
	case "validate":
		err := validateProject(ctx, args[1:], out, errOut, runner, workdir)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "version", "--version":
		if len(args) != 1 {
			return fmt.Errorf("%s accepts no additional arguments", args[0])
		}
		fmt.Fprintf(out, "gcp-free-deploy %s\n", currentVersion())
		return nil
	case "help", "-h", "--help":
		printUsage(out)
		return nil
	default:
		printUsage(errOut)
		return fmt.Errorf("unsupported command: %s", args[0])
	}
}

func parseInitOptions(args []string, errOut io.Writer) error {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	set.SetOutput(errOut)
	set.Usage = func() {
		fmt.Fprintln(errOut, "Usage: gcp-free-deploy init")
		fmt.Fprintln(errOut, "Prepare the embedded Terraform and example config files in the current working directory.")
	}
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("init accepts no additional arguments")
	}
	return nil
}

func parseUpOptions(args []string, errOut io.Writer) (upOptions, error) {
	var opts upOptions
	set := flag.NewFlagSet("up", flag.ContinueOnError)
	set.SetOutput(errOut)
	set.StringVar(&opts.ConfigPath, "config", defaultConfigPath, "deployment config JSON path")
	set.BoolVar(&opts.AutoApprove, "auto-approve", false, "skip the apply confirmation")
	set.BoolVar(&opts.PlanOnly, "plan-only", false, "validate and create a plan without applying it")
	set.BoolVar(&opts.AllowPublicHTTP, "allow-public-http", false, "allow source ranges covering the entire IPv4 internet only when explicitly requested")
	set.DurationVar(&opts.StartupTimeout, "startup-timeout", defaultStartupTimeout, "startup verification deadline; final checks and diagnostics can take up to 90s longer")
	set.Usage = func() {
		fmt.Fprintln(errOut, "Usage: gcp-free-deploy up [options]")
		fmt.Fprintln(errOut, "Plan, create, and verify a deployment using state in the current working directory.")
		fmt.Fprintln(errOut, "")
		fmt.Fprintln(errOut, "Options:")
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		return upOptions{}, err
	}
	if set.NArg() != 0 {
		return upOptions{}, fmt.Errorf("unknown arguments for up: %s", strings.Join(set.Args(), " "))
	}
	if opts.StartupTimeout < minStartupTimeout || opts.StartupTimeout > maxStartupTimeout {
		return upOptions{}, fmt.Errorf("startup-timeout must be between %s and %s", minStartupTimeout, maxStartupTimeout)
	}
	return opts, nil
}

func parseDownOptions(args []string, errOut io.Writer) (downOptions, error) {
	var opts downOptions
	set := flag.NewFlagSet("down", flag.ContinueOnError)
	set.SetOutput(errOut)
	set.BoolVar(&opts.AutoApprove, "auto-approve", false, "skip the destroy confirmation")
	set.Usage = func() {
		fmt.Fprintln(errOut, "Usage: gcp-free-deploy down [options]")
		fmt.Fprintln(errOut, "Delete resources tracked by state in the current working directory.")
		fmt.Fprintln(errOut, "")
		fmt.Fprintln(errOut, "Options:")
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		return downOptions{}, err
	}
	if set.NArg() != 0 {
		return downOptions{}, fmt.Errorf("unknown arguments for down: %s", strings.Join(set.Args(), " "))
	}
	return opts, nil
}

func deployTerraform(ctx context.Context, in io.Reader, out io.Writer, runner Runner, workdir string, opts upOptions) error {
	cfg, err := readOrPromptConfig(opts.ConfigPath, in, out)
	if err != nil {
		return &DeploymentError{Kind: FailureInvalidConfig, Operation: "config validation", Diagnostics: err.Error()}
	}
	if cfg.exposesHTTPToEveryone() && !opts.PlanOnly && !opts.AllowPublicHTTP {
		return &DeploymentError{
			Kind:        FailureConfirmationRequired,
			Operation:   "public HTTP exposure confirmation",
			Diagnostics: "the configured source ranges cover the entire IPv4 internet; specify --allow-public-http to continue",
		}
	}
	if err := ensureRuntimeAssets(workdir); err != nil {
		return err
	}
	if err := verifyManagedTerraformFiles(workdir); err != nil {
		return err
	}
	if err := requireTool(runner, "terraform"); err != nil {
		return err
	}
	if !opts.PlanOnly {
		if err := requireTool(runner, "gcloud"); err != nil {
			return err
		}
		if err := requireTool(runner, "curl"); err != nil {
			return err
		}
	}
	planPath := filepath.Join(workdir, applyPlanName)
	if err := preparePlanDestination(planPath); err != nil {
		return &DeploymentError{Kind: FailureUnsafeState, Operation: "plan destination safety check", Diagnostics: err.Error()}
	}
	defer os.Remove(planPath)
	printDeploymentSummary(out, cfg, opts)
	if err := runTerraformPreflight(ctx, runner, workdir); err != nil {
		return err
	}
	if err := guardUpState(ctx, runner, workdir, cfg); err != nil {
		return err
	}

	zones := append([]string{cfg.Zone}, cfg.FallbackZones...)
	for index, zone := range zones {
		attempt := cfg
		attempt.Zone = zone
		if _, err := writeTerraformVariables(workdir, attempt); err != nil {
			return err
		}

		fmt.Fprintf(out, "\n[%d/%d] Creating a plan for zone %s.\n", index+1, len(zones), zone)
		hasChanges, err := runTerraformPlan(ctx, runner, workdir, applyPlanName, false, out)
		if err != nil {
			return err
		}
		if opts.PlanOnly {
			if hasChanges {
				fmt.Fprintln(out, "\nPlan validation complete: no resources were changed.")
			} else {
				fmt.Fprintln(out, "\nPlan validation complete: no changes detected.")
			}
			return nil
		}
		if !hasChanges {
			fmt.Fprintln(out, "\nTerraform detected no infrastructure changes. Mutable Docker tags and GitHub branch contents are not refreshed automatically.")
			outputs, err := readTerraformOutputs(ctx, runner, workdir)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, "Verifying the existing deployment without applying an empty plan.")
			monitor := NewDeploymentMonitor(runner, out)
			monitor.startupTimeout = opts.StartupTimeout
			if err := monitor.Wait(ctx, outputs); err != nil {
				return fmt.Errorf("%w\nThe VM and network remain. Diagnose the issue, then clean them up with down", err)
			}
			fmt.Fprintf(out, "\nExisting deployment verification complete: %s\n", outputs.WebsiteURL.Value)
			return nil
		}

		approved := opts.AutoApprove
		if !approved {
			approved, err = confirm(in, out, "Enter yes to apply this plan and create resources that may incur charges: ")
			if err != nil {
				return err
			}
			if !approved {
				fmt.Fprintln(out, "Deployment cancelled; no resources were changed.")
				return nil
			}
		}
		if err := runTerraformApply(ctx, runner, workdir, applyPlanName, approved); err != nil {
			var deployErr *DeploymentError
			if errors.As(err, &deployErr) && deployErr.Kind == FailureZoneCapacity && index+1 < len(zones) {
				fmt.Fprintf(out, "Zone %s has insufficient capacity; trying the next zone in the same region.\n", zone)
				continue
			}
			return fmt.Errorf("%w\nSome resources may remain. Check with terraform state list and the down command", err)
		}

		outputs, err := readTerraformOutputs(ctx, runner, workdir)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "\nInfrastructure created. Verifying startup, container, and HTTP status.")
		monitor := NewDeploymentMonitor(runner, out)
		monitor.startupTimeout = opts.StartupTimeout
		if err := monitor.Wait(ctx, outputs); err != nil {
			return fmt.Errorf("%w\nThe VM and network remain. Diagnose the issue, then clean them up with down", err)
		}

		fmt.Fprintf(out, "\nDeployment verification complete: %s\n", outputs.WebsiteURL.Value)
		fmt.Fprintln(out, "When finished, run gcp-free-deploy down to delete the resources.")
		return nil
	}

	return &DeploymentError{Kind: FailureZoneCapacity, Operation: "terraform apply", Diagnostics: "all specified zones have insufficient capacity"}
}

func validateProject(ctx context.Context, args []string, out, errOut io.Writer, runner Runner, workdir string) error {
	set := flag.NewFlagSet("validate", flag.ContinueOnError)
	set.SetOutput(errOut)
	configPath := set.String("config", defaultConfigPath, "deployment config JSON path")
	set.Usage = func() {
		fmt.Fprintln(errOut, "Usage: gcp-free-deploy validate [options]")
		fmt.Fprintln(errOut, "Check the config and Terraform files without querying or changing GCP resources.")
		fmt.Fprintln(errOut, "")
		fmt.Fprintln(errOut, "Options:")
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("validate received unknown arguments")
	}
	return withWorkdirLock(workdir, func() error {
		cfg, err := LoadDeployConfig(*configPath)
		if err != nil {
			return &DeploymentError{Kind: FailureInvalidConfig, Operation: "config validation", Diagnostics: err.Error()}
		}
		if err := requireTool(runner, "terraform"); err != nil {
			return err
		}
		if err := ensureRuntimeAssets(workdir); err != nil {
			return err
		}
		if err := verifyManagedTerraformFiles(workdir); err != nil {
			return err
		}
		if err := runTerraformPreflight(ctx, runner, workdir); err != nil {
			return err
		}
		fmt.Fprintln(out, "Config and Terraform static validation passed. No GCP resources were queried or changed.")
		printFreeTierAssessment(out, cfg)
		return nil
	})
}

func guardUpState(ctx context.Context, runner Runner, workdir string, desired DeployConfig) error {
	workspace := runner.Run(ctx, Command{Name: "terraform", Args: []string{"workspace", "show", "-no-color"}, Dir: workdir})
	if workspace.ExitCode != 0 {
		return newUnsafeStateError("could not inspect the Terraform workspace")
	}
	if strings.TrimSpace(workspace.Stdout) != "default" {
		return newUnsafeStateError("non-default Terraform workspaces are not supported")
	}

	statePath := filepath.Join(workdir, "terraform.tfstate")
	info, err := os.Lstat(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			backupPath := filepath.Join(workdir, "terraform.tfstate.backup")
			if _, backupErr := os.Lstat(backupPath); backupErr == nil {
				return newUnsafeStateError("only terraform.tfstate.backup remains without an active state")
			} else if !errors.Is(backupErr, os.ErrNotExist) {
				return newUnsafeStateError("could not inspect terraform.tfstate.backup")
			}
			return nil
		}
		return newUnsafeStateError("could not inspect terraform.tfstate")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return newUnsafeStateError("terraform.tfstate is empty or is not a safe regular file")
	}

	stateList := runner.Run(ctx, Command{Name: "terraform", Args: []string{"state", "list"}, Dir: workdir})
	if stateList.ExitCode != 0 {
		return newUnsafeStateError("could not read Terraform state addresses")
	}
	if strings.TrimSpace(stateList.Stdout) == "" {
		return nil
	}
	if err := validateOwnedState(stateList.Stdout); err != nil {
		return newUnsafeStateError(err.Error())
	}

	variables, err := readTerraformVariables(filepath.Join(workdir, terraformVariablesName))
	if err != nil {
		return newUnsafeStateError("could not safely inspect the existing deployment variables file")
	}
	outputs, err := readTerraformOutputs(ctx, runner, workdir)
	if err != nil {
		return newUnsafeStateError("could not verify the project and VM from existing state outputs")
	}
	if err := validateDestroyTarget(outputs, variables); err != nil {
		return newUnsafeStateError(err.Error())
	}
	if err := validateDesiredStateTarget(desired, variables); err != nil {
		return newUnsafeStateError(err.Error())
	}
	return nil
}

func validateDesiredStateTarget(desired DeployConfig, existing terraformVariables) error {
	if desired.ProjectID != existing.ProjectID || regionFromZone(desired.Zone) != existing.Region {
		return fmt.Errorf("requested project or region does not match the existing state target")
	}
	allowedZones := append([]string{desired.Zone}, desired.FallbackZones...)
	for _, zone := range allowedZones {
		if zone == existing.Zone {
			return nil
		}
	}
	return fmt.Errorf("requested zone list does not include the existing state zone")
}

func newUnsafeStateError(reason string) error {
	return &DeploymentError{
		Kind:      FailureUnsafeState,
		Operation: "up state safety check",
		Diagnostics: reason + ". Do not delete the state. Inspect the actual GCP resources, then migrate the state or " +
			"run in an isolated new working directory",
	}
}

func readOrPromptConfig(path string, in io.Reader, out io.Writer) (DeployConfig, error) {
	cfg, err := LoadDeployConfig(path)
	if err == nil {
		fmt.Fprintf(out, "Using config file: %s\n", path)
		return cfg, nil
	}
	if !errors.Is(rootCause(err), os.ErrNotExist) {
		return DeployConfig{}, err
	}
	if path != defaultConfigPath {
		return DeployConfig{}, err
	}
	fmt.Fprintln(out, "gcp-free-deploy.json was not found; starting interactive configuration.")
	return promptDeployConfig(in, out)
}

func promptDeployConfig(in io.Reader, out io.Writer) (DeployConfig, error) {
	reader := bufio.NewReader(in)
	read := func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}

	var cfg DeployConfig
	var err error
	if cfg.ProjectID, err = read("GCP project ID: "); err != nil {
		return DeployConfig{}, err
	}
	if cfg.Zone, err = read("Primary zone (for example, us-central1-a): "); err != nil {
		return DeployConfig{}, err
	}
	fallbacks, err := read("Fallback zones in the same region (comma-separated, optional): ")
	if err != nil {
		return DeployConfig{}, err
	}
	if fallbacks != "" {
		cfg.FallbackZones = strings.Split(fallbacks, ",")
	}
	if cfg.Source, err = read("Deployment source (docker/github): "); err != nil {
		return DeployConfig{}, err
	}
	if strings.EqualFold(cfg.Source, "docker") {
		if cfg.DockerImage, err = read("Docker image with an explicit tag or digest: "); err != nil {
			return DeployConfig{}, err
		}
	} else {
		if cfg.GithubRepo, err = read("Public GitHub HTTPS URL: "); err != nil {
			return DeployConfig{}, err
		}
	}
	port, err := read("Container port (default: 80): ")
	if err != nil {
		return DeployConfig{}, err
	}
	if port == "" {
		cfg.ContainerPort = 80
	} else if cfg.ContainerPort, err = strconv.Atoi(port); err != nil {
		return DeployConfig{}, &ValidationError{Field: "container_port", Message: "must be a number"}
	}
	ranges, err := read("Allowed HTTP source IPv4 CIDRs (comma-separated, for example, your-public-IP/32): ")
	if err != nil {
		return DeployConfig{}, err
	}
	if ranges != "" {
		cfg.AllowedSourceRanges = strings.Split(ranges, ",")
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return DeployConfig{}, err
	}
	return cfg, nil
}

func printDeploymentSummary(out io.Writer, cfg DeployConfig, opts upOptions) {
	fmt.Fprintln(out, "\nPre-deployment summary")
	fmt.Fprintf(out, "- project: %s\n", cfg.ProjectID)
	fmt.Fprintf(out, "- region / zone: %s / %s\n", regionFromZone(cfg.Zone), cfg.Zone)
	if len(cfg.FallbackZones) > 0 {
		fmt.Fprintf(out, "- fallback zones: %s\n", strings.Join(cfg.FallbackZones, ", "))
	}
	fmt.Fprintf(out, "- VM: %s, boot disk: %dGB pd-standard\n", cfg.MachineType, cfg.DiskSizeGB)
	fmt.Fprintf(out, "- source: %s, host TCP/80 -> container TCP/%d\n", cfg.Source, cfg.ContainerPort)
	fmt.Fprintf(out, "- HTTP source ranges: %s\n", strings.Join(cfg.AllowedSourceRanges, ", "))
	fmt.Fprintf(out, "- startup timeout: %s (verification deadline; final checks and diagnostics may add up to %s)\n", opts.StartupTimeout, 2*diagnosticTimeout)
	fmt.Fprintln(out, "- resources: dedicated VPC, subnet, HTTP firewall, IAP SSH firewall, one VM with ephemeral external IP")
	fmt.Fprintln(out, "- VM service account: none; HTTPS: not configured")
	if cfg.exposesHTTPToEveryone() {
		fmt.Fprintln(out, "- WARNING: TCP/80 will be reachable from the entire IPv4 internet")
	}
	if strings.HasSuffix(strings.Split(cfg.DockerImage, "@")[0], ":latest") {
		fmt.Fprintln(out, "- WARNING: Docker latest tag is mutable; use a fixed tag or digest when possible")
	}
	printFreeTierAssessment(out, cfg)
	if opts.PlanOnly {
		fmt.Fprintln(out, "- mode: plan only (no resource mutation)")
	} else {
		fmt.Fprintln(out, "- cost: Free Tier eligibility is not guaranteed; VM, disk, IP, and network usage can be billed")
	}
}

func confirm(in io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprint(out, prompt)
	reader := bufio.NewReader(in)
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(answer), "yes"), nil
}

func destroyTerraform(ctx context.Context, in io.Reader, out io.Writer, runner Runner, workdir string, opts downOptions) error {
	statePath := filepath.Join(workdir, "terraform.tfstate")
	info, err := os.Lstat(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy safety check", Diagnostics: "terraform.tfstate does not exist, so the resources to delete cannot be verified"}
		}
		return fmt.Errorf("inspect state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy safety check", Diagnostics: "terraform.tfstate is empty or is not a regular file"}
	}
	planPath := filepath.Join(workdir, destroyPlanName)
	if err := preparePlanDestination(planPath); err != nil {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "plan destination safety check", Diagnostics: err.Error()}
	}
	defer os.Remove(planPath)
	if err := requireTool(runner, "terraform"); err != nil {
		return err
	}
	if err := ensureRuntimeAssets(workdir); err != nil {
		return err
	}
	if err := verifyManagedTerraformFilesForDestroy(workdir); err != nil {
		return err
	}
	if err := runTerraformPreflight(ctx, runner, workdir); err != nil {
		return err
	}
	workspace := runner.Run(ctx, Command{Name: "terraform", Args: []string{"workspace", "show", "-no-color"}, Dir: workdir})
	if workspace.ExitCode != 0 || strings.TrimSpace(workspace.Stdout) != "default" {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy safety check", Diagnostics: "only the default Terraform workspace can be destroyed"}
	}
	stateList := runner.Run(ctx, Command{Name: "terraform", Args: []string{"state", "list"}, Dir: workdir})
	if stateList.ExitCode != 0 {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy safety check", Diagnostics: "could not read Terraform state addresses"}
	}
	if err := validateOwnedState(stateList.Stdout); err != nil {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy safety check", Diagnostics: err.Error()}
	}

	variables, err := readTerraformVariables(filepath.Join(workdir, terraformVariablesName))
	if err != nil {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy safety check", Diagnostics: "could not safely inspect the deployment variables file: " + err.Error()}
	}
	target, err := readDestroyTarget(ctx, runner, workdir, stateList.Stdout, variables)
	if err != nil {
		return &DeploymentError{Kind: FailureUnsafeDestroy, Operation: "destroy safety check", Diagnostics: err.Error()}
	}

	fmt.Fprintln(out, "\nPre-destroy summary")
	fmt.Fprintf(out, "- project: %s\n", target.ProjectID)
	fmt.Fprintf(out, "- region / configured zone: %s / %s\n", target.Region, target.Zone)
	if target.VMCreated {
		fmt.Fprintf(out, "- VM: %s\n", target.VMName)
	} else {
		fmt.Fprintln(out, "- VM: not created (cleaning up partial-creation state)")
	}
	if len(target.Resources) > 0 {
		fmt.Fprintf(out, "- Terraform-managed resources: %s\n", strings.Join(target.Resources, ", "))
	}
	fmt.Fprintln(out, "- Only resources in this project managed by the local state will be deleted.")

	hasChanges, err := runTerraformPlan(ctx, runner, workdir, destroyPlanName, true, out)
	if err != nil {
		return err
	}
	if !hasChanges {
		fmt.Fprintln(out, "No Terraform resources to delete.")
		return nil
	}

	approved := opts.AutoApprove
	if !approved {
		approved, err = confirm(in, out, "Enter yes to delete the project resources listed above: ")
		if err != nil {
			return err
		}
		if !approved {
			fmt.Fprintln(out, "Destroy cancelled; no resources were deleted.")
			return nil
		}
	}
	if err := runTerraformDestroyApply(ctx, runner, workdir, destroyPlanName, approved); err != nil {
		return err
	}

	stateList = runner.Run(ctx, Command{Name: "terraform", Args: []string{"state", "list"}, Dir: workdir})
	if stateList.ExitCode != 0 || strings.TrimSpace(stateList.Stdout) != "" {
		diagnostics := sanitizeDiagnostics(stateList.Stdout + "\n" + stateList.Stderr)
		return &DeploymentError{
			Kind:        FailureTerraformDestroy,
			Operation:   "destroy result verification",
			Diagnostics: "some resources remain in state: " + diagnostics + ". Check the project and zone in the Google Cloud Console. Remaining resources may incur charges",
		}
	}
	fmt.Fprintln(out, "Destroy complete: no managed resources remain in Terraform state.")
	return nil
}

func validateDestroyTarget(outputs TerraformOutputs, variables terraformVariables) error {
	if variables.ProjectID == "" || variables.Region == "" || variables.Zone == "" {
		return fmt.Errorf("project, region, or zone is empty in the deployment variables file")
	}
	if variables.Region != regionFromZone(variables.Zone) {
		return fmt.Errorf("region and zone do not match in the deployment variables file")
	}
	if outputs.ProjectID.Value != variables.ProjectID {
		return fmt.Errorf("project in the Terraform state output does not match the deployment variables file")
	}
	if outputs.Region.Value != variables.Region || outputs.VMZone.Value != variables.Zone {
		return fmt.Errorf("region or zone in the Terraform state output does not match the deployment variables file")
	}
	if outputs.VMName.Value != "gcp-free-deploy-demo" && outputs.VMName.Value != "my-free-portfolio" {
		return fmt.Errorf("VM name is not verified as managed by this tool")
	}
	return nil
}

func validateOwnedState(raw string) error {
	allowed := map[string]bool{
		"google_compute_network.demo":     true,
		"google_compute_subnetwork.demo":  true,
		"google_compute_firewall.http":    true,
		"google_compute_firewall.iap_ssh": true,
		"google_compute_instance.demo":    true,
	}
	count := 0
	for _, address := range strings.Fields(raw) {
		count++
		if !allowed[address] {
			return fmt.Errorf("state contains an address that is not verified as owned by this tool: %s", address)
		}
	}
	if count == 0 {
		return fmt.Errorf("state contains no managed resource addresses")
	}
	return nil
}

func requireTool(runner Runner, name string) error {
	if err := runner.LookPath(name); err != nil {
		return fmt.Errorf("required tool %s was not found", name)
	}
	return nil
}

func rootCause(err error) error {
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err
		}
		err = unwrapped
	}
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: gcp-free-deploy <command> [options]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  init      Prepare the embedded Terraform and example config files")
	fmt.Fprintln(out, "  validate  Check the config and Terraform files without querying GCP")
	fmt.Fprintln(out, "  up        Plan, create, and verify the deployment")
	fmt.Fprintln(out, "  down      Delete resources tracked by this working directory's state")
	fmt.Fprintln(out, "  version   Print version information")
	fmt.Fprintln(out, "  help      Show this help")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Run init, validate, up, and down from the same working directory so Terraform state remains available for safe cleanup.")
	fmt.Fprintln(out, "Run gcp-free-deploy <command> --help for command options.")
}

func printFreeTierAssessment(out io.Writer, cfg DeployConfig) {
	region := regionFromZone(cfg.Zone)
	matchesMachine := cfg.MachineType == "e2-micro"
	matchesRegion := computeFreeTierRegions[region]

	switch {
	case matchesMachine && matchesRegion:
		fmt.Fprintf(out, "- Free Tier VM profile: matches e2-micro in eligible region %s (monthly limits and account eligibility still apply)\n", region)
	case !matchesMachine && !matchesRegion:
		fmt.Fprintf(out, "- Free Tier VM profile: does not match (machine type %s is not e2-micro and region %s is not an eligible Compute Engine Free Tier region)\n", cfg.MachineType, region)
	case !matchesMachine:
		fmt.Fprintf(out, "- Free Tier VM profile: does not match (machine type %s is not e2-micro)\n", cfg.MachineType)
	default:
		fmt.Fprintf(out, "- Free Tier VM profile: does not match (region %s is not an eligible Compute Engine Free Tier region)\n", region)
	}
	fmt.Fprintln(out, "- COST WARNING: this deployment uses a separately priced external IPv4 address, even when VM usage qualifies for the Free Tier")
}
