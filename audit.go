package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type auditInstance struct {
	Name        string `json:"name"`
	MachineType string `json:"machineType"`
	Zone        string `json:"zone"`
	Status      string `json:"status"`
	Disks       []struct {
		Source string `json:"source"`
	} `json:"disks"`
	NetworkInterfaces []struct {
		AccessConfigs []struct {
			NetworkTier string `json:"networkTier"`
		} `json:"accessConfigs"`
	} `json:"networkInterfaces"`
}

type auditDisk struct {
	Name   string      `json:"name"`
	Type   string      `json:"type"`
	Zone   string      `json:"zone"`
	SizeGB json.Number `json:"sizeGb"`
	Users  []string    `json:"users"`
}

// auditLiveCost reads actual resources; it does not infer a target from local
// defaults, write Terraform assets, or change cloud configuration.
func auditLiveCost(ctx context.Context, args []string, out, errOut io.Writer, runner Runner) error {
	set := flag.NewFlagSet("audit", flag.ContinueOnError)
	set.SetOutput(errOut)
	project := set.String("project", "", "required GCP project ID")
	vm := set.String("vm", "", "required existing VM name")
	zone := set.String("zone", "", "required existing VM zone")
	set.Usage = func() {
		fmt.Fprintln(errOut, "Usage: gcp-free-deploy audit --project PROJECT --vm VM --zone ZONE")
		fmt.Fprintln(errOut, "Read actual VM, disk, and network cost configuration with gcloud. No resources are changed.")
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 || !projectIDPattern.MatchString(*project) || !zonePattern.MatchString(*zone) || !regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(*vm) {
		return &DeploymentError{Kind: FailureInvalidConfig, Operation: "live cost audit options", Diagnostics: "explicit valid --project, --vm, and --zone are required; positional arguments are not supported"}
	}
	if err := requireTool(runner, "gcloud"); err != nil {
		return err
	}
	query := func(args []string, target any) error {
		queryCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		result := runner.Run(queryCtx, Command{Name: "gcloud", Args: append(args, "--project="+*project, "--quiet")})
		if result.ExitCode != 0 {
			return &DeploymentError{Kind: FailureKind("cost_audit_query"), Operation: "live cost audit", Diagnostics: "gcloud read failed; verify authentication and Compute Engine read permissions"}
		}
		if err := json.Unmarshal([]byte(result.Stdout), target); err != nil {
			return &DeploymentError{Kind: FailureKind("cost_audit_query"), Operation: "live cost audit", Diagnostics: "gcloud returned invalid or incomplete JSON"}
		}
		return nil
	}
	var instance auditInstance
	if err := query([]string{"compute", "instances", "describe", *vm, "--zone=" + *zone, "--format=json(name,machineType,zone,status,disks[].source,networkInterfaces[].accessConfigs[].networkTier)"}, &instance); err != nil {
		return err
	}
	var disks []auditDisk
	if err := query([]string{"compute", "disks", "list", "--format=json(name,type,zone,sizeGb,users)"}, &disks); err != nil {
		return err
	}
	var instances []auditInstance
	if err := query([]string{"compute", "instances", "list", "--format=json(name,machineType,zone,status)"}, &instances); err != nil {
		return err
	}
	var issues []string
	add := func(s string) { issues = append(issues, s) }
	fmt.Fprintln(out, "Live cost configuration audit (read-only)")
	fmt.Fprintf(out, "- project: %s; VM: %s; zone: %s; status: %s\n", *project, instance.Name, path.Base(instance.Zone), instance.Status)
	if instance.Name != *vm || path.Base(instance.Zone) != *zone || instance.Status == "" {
		add("VM identity or status is missing or inconsistent")
	}
	machine := path.Base(instance.MachineType)
	fmt.Fprintf(out, "- actual machine: %s\n", machine)
	if machine != "e2-micro" || !computeFreeTierRegions[regionFromZone(path.Base(instance.Zone))] {
		add("actual VM is outside the e2-micro / eligible US region profile")
	}
	if len(instance.NetworkInterfaces) == 0 {
		add("network interface information is missing")
	}
	addresses := 0
	networkKnown := len(instance.NetworkInterfaces) > 0
	for _, nic := range instance.NetworkInterfaces {
		if nic.AccessConfigs == nil {
			networkKnown = false
			add("external IPv4 access configuration is missing or unknown")
		}
		for _, access := range nic.AccessConfigs {
			addresses++
			fmt.Fprintf(out, "- actual external network tier: %s\n", access.NetworkTier)
			if access.NetworkTier != "STANDARD" {
				add("external network tier is not verified STANDARD; Standard's traffic allowance cannot be assumed")
			}
		}
	}
	if addresses == 0 && networkKnown {
		fmt.Fprintln(out, "- no external IPv4 access configuration; public IPv4 access is unavailable")
	}
	total := int64(0)
	diskKeys := map[string]bool{}
	for _, disk := range disks {
		size, err := strconv.ParseInt(string(disk.SizeGB), 10, 64)
		if err != nil || size <= 0 || size > 1000000000 {
			add("disk size is missing or invalid")
			continue
		}
		total += size
		diskZone := path.Base(disk.Zone)
		diskKeys[diskZone+"/"+disk.Name] = true
		if path.Base(disk.Type) != "pd-standard" || !computeFreeTierRegions[regionFromZone(diskZone)] {
			add("project contains a disk outside the zonal pd-standard / eligible US region profile")
		}
		if len(disk.Users) == 0 {
			add("project contains an unattached disk that still consumes storage allowance")
		}
	}
	fmt.Fprintf(out, "- project disk allocation: %d GB across %d disks (30 GB-month shared allowance)\n", total, len(disks))
	if total > 30 {
		add("project disk allocation exceeds 30 GB")
	}
	if len(instance.Disks) == 0 {
		add("VM disk information is missing")
	}
	for _, disk := range instance.Disks {
		parts := strings.Split(disk.Source, "/")
		if len(parts) < 4 || !diskKeys[parts[len(parts)-3]+"/"+parts[len(parts)-1]] {
			add("VM disk could not be verified in the project disk inventory")
		}
	}
	found := false
	for _, item := range instances {
		if item.Name == *vm && path.Base(item.Zone) == *zone {
			found = true
		}
	}
	if !found {
		add("target VM is missing from project inventory")
	}
	fmt.Fprintf(out, "- project VM count: %d\n", len(instances))
	if len(instances) > 1 {
		fmt.Fprintln(out, "- CAUTION: multiple VMs may compete for the account's free VM hours; stopped VMs can still retain billable disks and addresses")
	}
	fmt.Fprintln(out, "- Not a zero-cost guarantee: external IPv4 address-hours, outbound traffic, billing-account totals across other projects, and other services are not measured")
	fmt.Fprintln(out, "- This checks present allocation, not earlier monthly usage or actual invoices; budgets do not cap spending")
	for _, issue := range issues {
		fmt.Fprintf(out, "- ISSUE: %s\n", issue)
	}
	if len(issues) > 0 {
		return &DeploymentError{Kind: FailureKind("cost_audit_profile"), Operation: "live cost audit", Diagnostics: strings.Join(issues, "; ")}
	}
	fmt.Fprintln(out, "Result: inspected resources match the checked cost profile; monthly usage and other charges still apply.")
	return nil
}

// verifyLiveNetworkTier prevents a successful up from hiding deployed Premium
// access even when the managed Terraform template requests Standard.
func verifyLiveNetworkTier(ctx context.Context, runner Runner, outputs TerraformOutputs) error {
	fail := func(message string) error {
		return &DeploymentError{Kind: FailureKind("cost_audit_profile"), Operation: "live network cost verification", Diagnostics: message}
	}
	if !projectIDPattern.MatchString(outputs.ProjectID.Value) || !zonePattern.MatchString(outputs.VMZone.Value) || !regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(outputs.VMName.Value) {
		return fail("Terraform output contains an invalid project, VM name, or zone")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result := runner.Run(queryCtx, Command{Name: "gcloud", Args: []string{"compute", "instances", "describe", outputs.VMName.Value, "--project=" + outputs.ProjectID.Value, "--zone=" + outputs.VMZone.Value, "--format=json(name,zone,networkInterfaces[].accessConfigs[].networkTier)", "--quiet"}})
	if result.ExitCode != 0 {
		return fail("could not read actual network tier; verify gcloud authentication and Compute Engine read permissions")
	}
	var instance auditInstance
	if err := json.Unmarshal([]byte(result.Stdout), &instance); err != nil {
		return fail("gcloud returned invalid network configuration JSON")
	}
	if instance.Name != outputs.VMName.Value || path.Base(instance.Zone) != outputs.VMZone.Value || len(instance.NetworkInterfaces) == 0 {
		return fail("actual VM identity or network configuration is missing or inconsistent")
	}
	addresses := 0
	for _, nic := range instance.NetworkInterfaces {
		if nic.AccessConfigs == nil {
			return fail("actual external network configuration is unknown")
		}
		for _, access := range nic.AccessConfigs {
			addresses++
			if access.NetworkTier != "STANDARD" {
				return fail("actual external network tier is not verified STANDARD; run audit to inspect the deployed configuration")
			}
		}
	}
	if addresses == 0 {
		return fail("expected public access configuration is absent")
	}
	return nil
}

// guardProjectVMOverlap catches legacy or separately managed VMs before a new
// deployment can consume another share of the billing account's free VM hours.
// It deliberately checks stopped VMs too: they can be restarted independently.
func guardProjectVMOverlap(ctx context.Context, runner Runner, cfg DeployConfig) error {
	fail := func(message string) error {
		return &DeploymentError{Kind: FailureKind("cost_audit_profile"), Operation: "project VM overlap safety check", Diagnostics: message + ". Inspect existing resources with audit instead of creating another deployment; --allow-paid-resources deliberately overrides this check"}
	}
	if !projectIDPattern.MatchString(cfg.ProjectID) || !zonePattern.MatchString(cfg.Zone) {
		return fail("project or zone is invalid")
	}
	allowedZones := map[string]bool{cfg.Zone: true}
	for _, zone := range cfg.FallbackZones {
		if !zonePattern.MatchString(zone) {
			return fail("fallback zone is invalid")
		}
		allowedZones[zone] = true
	}
	queryCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result := runner.Run(queryCtx, Command{Name: "gcloud", Args: []string{"compute", "instances", "list", "--project=" + cfg.ProjectID, "--format=json(name,zone,status)", "--quiet"}})
	if result.ExitCode != 0 {
		return fail("could not verify existing project VMs; check authentication and read permissions")
	}
	var instances []auditInstance
	if err := json.Unmarshal([]byte(result.Stdout), &instances); err != nil || instances == nil {
		return fail("project VM inventory is invalid or incomplete")
	}
	for _, instance := range instances {
		zone := path.Base(instance.Zone)
		if !regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(instance.Name) || !zonePattern.MatchString(zone) || instance.Status == "" {
			return fail("project VM inventory contains missing or invalid identity/status")
		}
		if instance.Name != "gcp-free-deploy-demo" || !allowedZones[zone] {
			return fail("an existing VM can consume the shared free VM hours")
		}
	}
	if len(instances) > 1 {
		return fail("multiple existing VMs can consume the shared free VM hours")
	}
	return nil
}
