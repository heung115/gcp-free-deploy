package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeployConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		wantField string
		cfg       DeployConfig
	}{
		{
			name:      "invalid project id",
			wantField: "project_id",
			cfg: DeployConfig{
				ProjectID:           "123456789",
				Zone:                "us-central1-a",
				Source:              "docker",
				DockerImage:         "nginx:1.27.4",
				ContainerPort:       80,
				AllowedSourceRanges: []string{"203.0.113.10/32"},
			},
		},
		{
			name:      "invalid zone",
			wantField: "zone",
			cfg: DeployConfig{
				ProjectID:           "demo-project-123",
				Zone:                "us-central1-a;reboot",
				Source:              "docker",
				DockerImage:         "nginx:1.27.4",
				ContainerPort:       80,
				AllowedSourceRanges: []string{"203.0.113.10/32"},
			},
		},
		{
			name:      "invalid container port",
			wantField: "container_port",
			cfg: DeployConfig{
				ProjectID:           "demo-project-123",
				Zone:                "us-central1-a",
				Source:              "docker",
				DockerImage:         "nginx:1.27.4",
				ContainerPort:       65536,
				AllowedSourceRanges: []string{"203.0.113.10/32"},
			},
		},
		{
			name:      "unsafe docker image",
			wantField: "docker_image",
			cfg: DeployConfig{
				ProjectID:           "demo-project-123",
				Zone:                "us-central1-a",
				Source:              "docker",
				DockerImage:         "nginx:latest;reboot",
				ContainerPort:       80,
				AllowedSourceRanges: []string{"203.0.113.10/32"},
			},
		},
		{
			name:      "docker image without tag or digest",
			wantField: "docker_image",
			cfg: DeployConfig{
				ProjectID:           "demo-project-123",
				Zone:                "us-central1-a",
				Source:              "docker",
				DockerImage:         "ghcr.io/example/demo",
				ContainerPort:       80,
				AllowedSourceRanges: []string{"203.0.113.10/32"},
			},
		},
		{
			name:      "non github URL",
			wantField: "github_repo",
			cfg: DeployConfig{
				ProjectID:           "demo-project-123",
				Zone:                "us-central1-a",
				Source:              "github",
				GithubRepo:          "https://example.com/owner/repo.git",
				ContainerPort:       80,
				AllowedSourceRanges: []string{"203.0.113.10/32"},
			},
		},
		{
			name:      "missing source range",
			wantField: "allowed_source_ranges",
			cfg: DeployConfig{
				ProjectID:     "demo-project-123",
				Zone:          "us-central1-a",
				Source:        "docker",
				DockerImage:   "nginx:1.27.4",
				ContainerPort: 80,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.Normalize()
			err := tt.cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil for an invalid configuration")
			}
			validationErr, ok := err.(*ValidationError)
			if !ok || validationErr.Field != tt.wantField {
				t.Fatalf("Validate() error = %#v, want field %q", err, tt.wantField)
			}
		})
	}
}

func TestDeployConfigValidationAcceptsSupportedSources(t *testing.T) {
	tests := []DeployConfig{
		{
			ProjectID:           "demo-project-123",
			Zone:                "us-central1-a",
			Source:              "docker",
			DockerImage:         "ghcr.io/example/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContainerPort:       3000,
			AllowedSourceRanges: []string{"203.0.113.10/32"},
		},
		{
			ProjectID:           "demo-project-123",
			Zone:                "asia-northeast3-a",
			Source:              "github",
			GithubRepo:          "https://github.com/example/demo-app.git",
			ContainerPort:       8080,
			AllowedSourceRanges: []string{"198.51.100.0/24"},
		},
	}

	for _, cfg := range tests {
		cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() returned an error for a valid configuration: %v", err)
		}
	}
}

func TestLoadDeployConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"project_id":"demo-project-123",
		"zone":"us-central1-a",
		"source":"docker",
		"docker_image":"nginx:1.27.4",
		"container_port":80,
		"allowed_source_ranges":["203.0.113.10/32"],
		"unexpected":true
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadDeployConfig(path); err == nil {
		t.Fatal("LoadDeployConfig() accepted an unknown field")
	}
}

func TestDeployConfigFallbackZonesStayInOneRegion(t *testing.T) {
	cfg := DeployConfig{
		ProjectID:           "demo-project-123",
		Zone:                "us-central1-a",
		FallbackZones:       []string{"us-east1-b"},
		Source:              "docker",
		DockerImage:         "nginx:1.27.4",
		ContainerPort:       80,
		AllowedSourceRanges: []string{"203.0.113.10/32"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a fallback zone from another region")
	}
}

func TestExposesHTTPToEveryoneRecognizesNonCanonicalSlashZero(t *testing.T) {
	cfg := DeployConfig{AllowedSourceRanges: []string{"203.0.113.10/0"}}
	if !cfg.exposesHTTPToEveryone() {
		t.Fatal("exposesHTTPToEveryone() missed an IPv4 /0 range")
	}
}
