package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// costProfileIssues checks validated configuration, not billing-account usage.
// Disk size is already limited to 10–30 GB by DeployConfig.Validate; the
// managed Terraform template fixes disk type to pd-standard.
func costProfileIssues(cfg DeployConfig) []string {
	var issues []string
	if cfg.MachineType != "e2-micro" {
		issues = append(issues, "machine_type must be e2-micro for the Free Tier VM profile")
	}
	if !computeFreeTierRegions[regionFromZone(cfg.Zone)] {
		issues = append(issues, "zone must be in us-west1, us-central1, or us-east1 for the Free Tier VM profile (keep fallback_zones in the same region)")
	}
	return issues
}

func guardCostProfile(cfg DeployConfig) error {
	if issues := costProfileIssues(cfg); len(issues) > 0 {
		return &DeploymentError{
			Kind:        FailureConfirmationRequired,
			Operation:   "cost profile safety check",
			Diagnostics: strings.Join(issues, "; ") + ". Change the config or explicitly specify --allow-paid-resources on up. --auto-approve does not bypass this check; --plan-only remains available",
		}
	}
	return nil
}

// checkCostProfile deliberately has no Runner or workdir lock: it only reads
// the requested config and must work without cloud tools or writable state.
func checkCostProfile(args []string, out, errOut io.Writer) error {
	set := flag.NewFlagSet("cost", flag.ContinueOnError)
	set.SetOutput(errOut)
	configPath := set.String("config", defaultConfigPath, "deployment config JSON path")
	set.Usage = func() {
		fmt.Fprintln(errOut, "Usage: gcp-free-deploy cost [options]")
		fmt.Fprintln(errOut, "Check the requested configuration offline; no tools, cloud queries, or file writes.")
		fmt.Fprintln(errOut, "Exit successfully only if valid configuration matches the Free Tier VM profile; this is not a bill estimate.")
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("cost received unknown arguments")
	}
	cfg, err := LoadDeployConfig(*configPath)
	if err != nil {
		return &DeploymentError{Kind: FailureInvalidConfig, Operation: "config validation", Diagnostics: err.Error()}
	}
	fmt.Fprintln(out, "Offline cost profile (requested configuration only)")
	printFreeTierAssessment(out, cfg)
	fmt.Fprintf(out, "- boot disk: %d GB pd-standard; other disks share the account's monthly allowance\n", cfg.DiskSizeGB)
	fmt.Fprintln(out, "- Not a bill estimate or a zero-cost guarantee: live resources, billing-account usage, credits, and prices were not queried")
	if cfg.MaxRuntimeHours > 0 {
		fmt.Fprintf(out, "- automatic stop: %d hours per start; disks remain and restarting resets the limit\n", cfg.MaxRuntimeHours)
	} else {
		fmt.Fprintln(out, "- automatic stop is disabled; set max_runtime_hours (1-168) for disposable demos")
	}
	fmt.Fprintln(out, "- Run down from the deployment working directory when finished; stopping alone does not remove disks")
	if err := guardCostProfile(cfg); err != nil {
		fmt.Fprintln(out, "Result: outside the Free Tier VM profile; up is blocked by default.")
		return err
	}
	fmt.Fprintln(out, "Result: matches the Free Tier VM profile; account-wide limits and other charges still apply.")
	return nil
}
