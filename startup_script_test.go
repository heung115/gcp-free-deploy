package main

import (
	"strings"
	"testing"
)

func TestStartupScriptReusesCompletedDeploymentAcrossReboots(t *testing.T) {
	terraform := readRepositoryFile(t, "main.tf")
	restartStart := strings.Index(terraform, `if [[ -f "$COMPLETED_MARKER" ]]`)
	if restartStart == -1 {
		t.Fatal("startup script does not check its completion marker")
	}

	provisionOffset := strings.Index(terraform[restartStart:], "\n    else\n")
	if provisionOffset == -1 {
		t.Fatal("startup script does not separate restart and initial provisioning paths")
	}
	provisionStart := restartStart + provisionOffset
	restartPath := terraform[restartStart:provisionStart]
	if !strings.Contains(restartPath, "docker start web") {
		t.Error("completed-boot path does not restart the existing container")
	}
	if !strings.Contains(restartPath, `rm -f "$COMPLETED_MARKER"`) {
		t.Error("completed-boot path does not clear a stale marker when the container is missing")
	}
	for _, unexpected := range []string{"docker pull", "git clone", "docker build"} {
		if strings.Contains(restartPath, unexpected) {
			t.Errorf("completed-boot path unexpectedly contains %q", unexpected)
		}
	}

	healthStart := strings.Index(terraform[provisionStart:], "CURRENT_STEP=http_health")
	if healthStart == -1 {
		t.Fatal("startup script has no shared health-check path")
	}
	healthStart += provisionStart
	provisionPath := terraform[provisionStart:healthStart]
	for _, required := range []string{"docker pull", "git clone", "docker build", "docker run"} {
		if !strings.Contains(provisionPath, required) {
			t.Errorf("initial provisioning path does not contain %q", required)
		}
	}
	if !strings.Contains(provisionPath, "apt_retry apt-get") {
		t.Error("initial provisioning does not retry transient APT failures")
	}

	healthPath := terraform[healthStart:]
	markerWrite := strings.Index(healthPath, `touch "$COMPLETED_MARKER"`)
	healthSuccess := strings.Index(healthPath, "HTTP_HEALTH_OK")
	startupDone := strings.Index(healthPath, "STARTUP_DONE")
	if healthSuccess == -1 || markerWrite <= healthSuccess || startupDone <= markerWrite {
		t.Fatal("completion marker must be written after health succeeds and before STARTUP_DONE")
	}
}
