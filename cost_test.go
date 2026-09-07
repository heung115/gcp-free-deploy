package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func saveCostConfig(t *testing.T, dir string, cfg DeployConfig) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCostOfflineProfile(t *testing.T) {
	for _, tc := range []struct {
		name, zone, machine string
		disk                int
		wantError           bool
	}{
		{"central", "us-central1-a", "e2-micro", 10, false},
		{"east", "us-east1-b", "e2-micro", 30, false},
		{"west defaults", "us-west1-a", "", 0, false},
		{"paid region", "asia-northeast3-a", "e2-micro", 10, true},
		{"paid machine", "us-central1-a", "e2-small", 10, true},
		{"both paid", "europe-west1-b", "e2-small", 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := testDeployConfig()
			cfg.Zone, cfg.MachineType, cfg.DiskSizeGB = tc.zone, tc.machine, tc.disk
			path := saveCostConfig(t, dir, cfg)
			var out bytes.Buffer
			// A nil runner would panic on any attempt to find or execute a tool.
			err := runCLI(context.Background(), []string{"cost", "--config", path}, nil, &out, &bytes.Buffer{}, nil, dir)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
			for _, want := range []string{"external IPv4", "Not a bill estimate", "account", "pd-standard", "Run down"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q in %s", want, out.String())
				}
			}
			if tc.wantError && !strings.Contains(err.Error(), "--allow-paid-resources") {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("cost wrote files: %v, %v", entries, err)
			}
		})
	}
}

func TestCostRejectsInvalidInputWithoutTools(t *testing.T) {
	dir := t.TempDir()
	cfg := testDeployConfig()
	cfg.DiskSizeGB = 31
	path := saveCostConfig(t, dir, cfg)
	for _, args := range [][]string{
		{"cost", "--config", path},
		{"cost", "--config", filepath.Join(dir, "missing.json")},
		{"cost", "unexpected"},
		{"cost", "--allow-paid-resources"},
	} {
		if err := runCLI(context.Background(), args, nil, &bytes.Buffer{}, &bytes.Buffer{}, nil, dir); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := runCLI(context.Background(), []string{"cost", "--help"}, nil, &bytes.Buffer{}, &bytes.Buffer{}, nil, dir); err != nil {
		t.Fatal(err)
	}
}

func TestUpCostGuardBeforeAssetsAndTools(t *testing.T) {
	for _, machine := range []string{"e2-micro", "e2-small"} {
		t.Run(machine, func(t *testing.T) {
			dir := t.TempDir()
			cfg := testDeployConfig()
			cfg.MachineType = machine
			if machine == "e2-micro" {
				cfg.Zone = "asia-northeast3-a"
			}
			path := saveCostConfig(t, dir, cfg)
			opts, err := parseUpOptions([]string{"--config", path, "--auto-approve"}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			err = deployTerraform(context.Background(), nil, &bytes.Buffer{}, nil, dir, opts)
			var deployErr *DeploymentError
			if !errors.As(err, &deployErr) || deployErr.Operation != "cost profile safety check" {
				t.Fatalf("unexpected error: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("guard wrote assets: %v, %v", entries, err)
			}
		})
	}
}

func TestUpAllowsPaidOverrideAndReadOnlyPlan(t *testing.T) {
	for _, option := range []string{"--allow-paid-resources", "--plan-only"} {
		t.Run(option, func(t *testing.T) {
			dir := t.TempDir()
			cfg := testDeployConfig()
			cfg.MachineType = "e2-small"
			path := saveCostConfig(t, dir, cfg)
			opts, err := parseUpOptions([]string{"--config", path, option}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			// Reach preflight and complete a no-change plan without applying.
			runner := &recordingRunner{results: []CommandResult{
				{}, {}, {}, {Stdout: "default\n"},
			}}
			if option == "--allow-paid-resources" {
				// Stop at tool discovery to avoid monitoring a real deployment.
				runner.missing = map[string]bool{"terraform": true}
			}
			err = deployTerraform(context.Background(), nil, &bytes.Buffer{}, runner, dir, opts)
			if option == "--allow-paid-resources" {
				if err == nil || !strings.Contains(err.Error(), "required tool terraform") {
					t.Fatalf("override did not reach tools: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("plan-only failed: %v", err)
				}
				for _, command := range runner.commands {
					if len(command.Args) > 0 && command.Args[0] == "apply" {
						t.Fatal("plan-only applied resources")
					}
				}
			}
		})
	}
}

func TestPaidOverrideDoesNotAllowPublicHTTP(t *testing.T) {
	dir := t.TempDir()
	cfg := testDeployConfig()
	cfg.MachineType = "e2-small"
	cfg.AllowedSourceRanges = []string{"0.0.0.0/0"}
	opts := upOptions{ConfigPath: saveCostConfig(t, dir, cfg), AllowPaidResources: true, AutoApprove: true}
	err := deployTerraform(context.Background(), nil, &bytes.Buffer{}, nil, dir, opts)
	if err == nil || !strings.Contains(err.Error(), "--allow-public-http") {
		t.Fatalf("public HTTP guard bypassed: %v", err)
	}
}
