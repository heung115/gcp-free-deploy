package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeLimitConfigBounds(t *testing.T) {
	for _, hours := range []int{-1, 0, 1, 24, 168, 169} {
		t.Run(fmt.Sprint(hours), func(t *testing.T) {
			cfg := DeployConfig{
				ProjectID: "demo-project-123", Zone: "us-central1-a",
				Source: "docker", DockerImage: "nginx:1.30.4", ContainerPort: 80,
				AllowedSourceRanges: []string{"203.0.113.10/32"}, MaxRuntimeHours: hours,
			}
			cfg.Normalize()
			err := cfg.Validate()
			if hours >= 0 && hours <= 168 {
				if err != nil {
					t.Fatal(err)
				}
			} else if invalid, ok := err.(*ValidationError); !ok || invalid.Field != "max_runtime_hours" {
				t.Fatalf("Validate() = %v, want max_runtime_hours rejection", err)
			}
		})
	}
}

func TestRuntimeLimitJSONAndTerraformRoundTrip(t *testing.T) {
	for _, field := range []string{"", `,"max_runtime_hours":0`, `,"max_runtime_hours":24`, `,"max_runtime_hours":168`} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			data := `{"project_id":"demo-project-123","zone":"us-central1-a","source":"docker","docker_image":"nginx:1.30.4","container_port":80,"allowed_source_ranges":["203.0.113.10/32"]` + field + `}`
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadDeployConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			varsPath, err := writeTerraformVariables(dir, cfg)
			if err != nil {
				t.Fatal(err)
			}
			vars, err := readTerraformVariables(varsPath)
			if err != nil {
				t.Fatal(err)
			}
			var input struct {
				Hours int `json:"max_runtime_hours"`
			}
			if err := json.Unmarshal([]byte(data), &input); err != nil {
				t.Fatal(err)
			}
			if vars.MaxRuntimeHours != input.Hours {
				t.Fatalf("runtime = %d, want %d", vars.MaxRuntimeHours, input.Hours)
			}
		})
	}
}

func TestRuntimeLimitOldSnapshotRemainsUnlimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), terraformVariablesName)
	if err := os.WriteFile(path, []byte(`{"project_id":"demo-project-123","zone":"us-central1-a"}`), 0600); err != nil {
		t.Fatal(err)
	}
	vars, err := readTerraformVariables(path)
	if err != nil {
		t.Fatal(err)
	}
	if vars.MaxRuntimeHours != 0 {
		t.Fatalf("old snapshot runtime = %d, want 0", vars.MaxRuntimeHours)
	}
}

func TestRuntimeLimitRejectsFractionalHours(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"max_runtime_hours":1.5}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeployConfig(path); err == nil {
		t.Fatal("fractional hours accepted")
	}
}
