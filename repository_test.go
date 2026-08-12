package main

import (
	"errors"
	"os/exec"
	"testing"
)

func TestSensitiveRuntimeFilesAreIgnored(t *testing.T) {
	paths := []string{
		"gcp-free-deploy",
		"terraform.tfstate",
		"terraform.tfstate.backup",
		"terraform.tfvars",
		"local.auto.tfvars",
		"local.auto.tfvars.json",
		"gcp-free-deploy.json",
		"credentials.json",
		"service-account-demo.json",
		"deploy-credentials.json",
		".env",
		".env.local",
		"private.key",
		"certificate.pem",
		".cloudflared/tunnel.json",
		"local.tfplan",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			cmd := exec.Command("git", "check-ignore", "--no-index", "--quiet", path)
			if err := cmd.Run(); err != nil {
				t.Fatalf("%s is not protected by .gitignore: %v", path, err)
			}
		})
	}
}

func TestExamplesAndDependencyLockRemainTrackable(t *testing.T) {
	for _, path := range []string{"gcp-free-deploy.example.json", ".env.example", ".terraform.lock.hcl"} {
		t.Run(path, func(t *testing.T) {
			cmd := exec.Command("git", "check-ignore", "--no-index", "--quiet", path)
			err := cmd.Run()
			if err == nil {
				t.Fatalf("%s is unexpectedly ignored", path)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("git check-ignore failed unexpectedly for %s: %v", path, err)
			}
		})
	}
}
