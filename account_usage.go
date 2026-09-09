package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

// checkAccountFreeUsage inventories current Compute resources, not historical
// billed usage. Listing from Billing avoids silently omitting invisible projects.
func checkAccountFreeUsage(ctx context.Context, runner Runner, out io.Writer, account string, expectedProjects ...string) error {
	if !regexp.MustCompile(`^[A-Fa-f0-9]{6}-[A-Fa-f0-9]{6}-[A-Fa-f0-9]{6}$`).MatchString(account) {
		return fmt.Errorf("invalid billing account")
	}
	query := func(args []string, target any) error {
		c, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		r := runner.Run(c, Command{Name: "gcloud", Args: append(args, "--quiet")})
		if r.ExitCode != 0 || strings.TrimSpace(r.Stdout) == "null" || json.Unmarshal([]byte(r.Stdout), target) != nil {
			return fmt.Errorf("inventory unavailable (permissions, disabled API, or incomplete response)")
		}
		return nil
	}
	fmt.Fprintln(out, "Checking existing Compute usage across the linked billing account (read-only).")
	fmt.Fprintln(out, "Free allowances are shared. Current resources do not prove past monthly usage, transfer usage or a zero bill; other services are outside this scan.")
	var projects []struct {
		ProjectID          string `json:"projectId"`
		BillingAccountName string `json:"billingAccountName"`
		BillingEnabled     bool   `json:"billingEnabled"`
	}
	if err := query([]string{"billing", "projects", "list", "--billing-account=" + account, "--format=json(projectId,billingAccountName,billingEnabled)"}, &projects); err != nil {
		return fmt.Errorf("cannot enumerate all projects on this billing account; usage is unknown")
	}
	seen := map[string]bool{}
	var issues []string
	resources := 0
	for _, p := range projects {
		if !projectIDPattern.MatchString(p.ProjectID) || p.BillingAccountName != "billingAccounts/"+account || !p.BillingEnabled {
			issues = append(issues, "incomplete billing project membership")
			continue
		}
		if seen[p.ProjectID] {
			continue
		}
		seen[p.ProjectID] = true
		fmt.Fprintf(out, "Project: %s\n", p.ProjectID)
		var vms []auditInstance
		if e := query([]string{"compute", "instances", "list", "--project=" + p.ProjectID, "--format=json(name,machineType,zone,status,networkInterfaces[].accessConfigs[].networkTier)"}, &vms); e != nil {
			fmt.Fprintln(out, "  VMs: UNKNOWN")
			issues = append(issues, p.ProjectID+": VM usage unknown")
		} else {
			fmt.Fprintf(out, "  VMs: %d (including stopped instances)\n", len(vms))
			resources += len(vms)
			for _, v := range vms {
				if v.Name == "" || v.MachineType == "" || v.Zone == "" || v.Status == "" {
					issues = append(issues, p.ProjectID+": incomplete VM data")
				}
				profile := "outside free VM shape"
				if path.Base(v.MachineType) == "e2-micro" && computeFreeTierRegions[regionFromZone(path.Base(v.Zone))] {
					profile = "free-tier VM shape; actual entitlement/usage unverified"
				}
				ipCount := 0
				for _, n := range v.NetworkInterfaces {
					ipCount += len(n.AccessConfigs)
				}
				if v.NetworkInterfaces == nil {
					issues = append(issues, p.ProjectID+": external VM IP configuration unknown")
					fmt.Fprintf(out, "    %s: %s, %s, %s; external access configs UNKNOWN\n", v.Name, v.Status, path.Base(v.MachineType), profile)
				} else {
					fmt.Fprintf(out, "    %s: %s, %s, %s; external access configs %d\n", v.Name, v.Status, path.Base(v.MachineType), profile, ipCount)
				}
			}
		}
		var disks []auditDisk
		if e := query([]string{"compute", "disks", "list", "--project=" + p.ProjectID, "--format=json(name,type,zone,sizeGb,users)"}, &disks); e != nil {
			fmt.Fprintln(out, "  Disks: UNKNOWN")
			issues = append(issues, p.ProjectID+": disk usage unknown")
		} else {
			resources += len(disks)
			var total int64
			for _, d := range disks {
				n, e := d.SizeGB.Int64()
				if e != nil || n <= 0 || d.Name == "" || d.Type == "" {
					issues = append(issues, p.ProjectID+": incomplete disk data")
					continue
				}
				total += n
				fmt.Fprintf(out, "    Disk %s: %d GB %s, attached to %d VM(s)\n", d.Name, n, path.Base(d.Type), len(d.Users))
			}
			fmt.Fprintf(out, "  Disks: %d, %d GB currently provisioned (not GB-month used)\n", len(disks), total)
		}
		var addresses []struct {
			Name        string `json:"name"`
			AddressType string `json:"addressType"`
			Status      string `json:"status"`
		}
		if e := query([]string{"compute", "addresses", "list", "--project=" + p.ProjectID, "--format=json(name,addressType,status)"}, &addresses); e != nil {
			fmt.Fprintln(out, "  Reserved IPs: UNKNOWN")
			issues = append(issues, p.ProjectID+": reserved IP usage unknown")
		} else {
			count := 0
			for _, a := range addresses {
				if a.Name == "" || a.AddressType == "" || a.Status == "" {
					issues = append(issues, p.ProjectID+": incomplete address data")
				}
				if a.AddressType == "EXTERNAL" {
					count++
					resources++
				}
			}
			fmt.Fprintf(out, "  Reserved external IPs: %d (separate from VM access configs; may overlap)\n", count)
		}
	}
	for _, expected := range expectedProjects {
		if !seen[expected] {
			issues = append(issues, "selected project missing from billing inventory; account membership or propagation is unresolved")
		}
	}
	if resources > 0 {
		issues = append(issues, "existing resources may already consume free allowances; another deployment can add charges")
	}
	if len(issues) > 0 {
		return fmt.Errorf("%s", strings.Join(issues, "; "))
	}
	fmt.Fprintf(out, "No current VM, disk or reserved external IP found in %d fully queried projects. This is not a monthly free-quota balance.\n", len(seen))
	return nil
}

func reviewAccountFreeUsage(ctx context.Context, runner Runner, out io.Writer, account string, ask func(string) (string, error), expectedProjects ...string) error {
	if err := checkAccountFreeUsage(ctx, runner, out, account, expectedProjects...); err != nil {
		fmt.Fprintf(out, "Cost check needs attention: %v\nInspect existing resources before adding another deployment. No resources were changed by this scan.\n", err)
		answer, e := ask("Enter allow-cost-risk to proceed despite existing or unknown usage; anything else cancels: ")
		if e != nil {
			return e
		}
		if answer != "allow-cost-risk" {
			return fmt.Errorf("deployment stopped after account cost check; existing resources unchanged")
		}
	}
	return nil
}
