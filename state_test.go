package main

import (
	"strings"
	"testing"
)

func TestParseDestroyTargetRejectsMismatchedStateProject(t *testing.T) {
	state := strings.Replace(partialTerraformStateJSON(), "demo-project-123", "different-project", 1)
	_, err := parseDestroyTarget([]byte(state), "google_compute_network.demo\ngoogle_compute_subnetwork.demo\ngoogle_compute_firewall.http\ngoogle_compute_firewall.iap_ssh\n", terraformVariables{
		ProjectID: "demo-project-123",
		Region:    "us-central1",
		Zone:      "us-central1-a",
	})
	if err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("parseDestroyTarget() error = %v, want project mismatch", err)
	}
}

func TestParseDestroyTargetRejectsStateListJSONMismatch(t *testing.T) {
	_, err := parseDestroyTarget([]byte(partialTerraformStateJSON()), "google_compute_network.demo\n", terraformVariables{
		ProjectID: "demo-project-123",
		Region:    "us-central1",
		Zone:      "us-central1-a",
	})
	if err == nil {
		t.Fatal("parseDestroyTarget() accepted resources absent from terraform state list")
	}
}
