package main

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseWorkflowExcludesUnsupportedWindowsARM64(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	if strings.Contains(workflow, "goos: [linux, windows, darwin]") && strings.Contains(workflow, "goarch: [amd64, arm64]") {
		t.Fatal("release matrix still produces unsupported windows/arm64")
	}

	want := map[string]bool{
		"linux/amd64":   true,
		"linux/arm64":   true,
		"windows/amd64": true,
		"darwin/amd64":  true,
		"darwin/arm64":  true,
	}
	got := make(map[string]bool)
	var goos string
	for _, line := range strings.Split(workflow, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- goos: ") {
			goos = strings.TrimSpace(strings.TrimPrefix(line, "- goos: "))
			continue
		}
		if goos != "" && strings.HasPrefix(line, "goarch: ") {
			goarch := strings.TrimSpace(strings.TrimPrefix(line, "goarch: "))
			got[goos+"/"+goarch] = true
			goos = ""
		}
	}
	if len(got) != len(want) {
		t.Fatalf("release targets = %#v, want %#v", got, want)
	}
	for target := range want {
		if !got[target] {
			t.Fatalf("release target %s is missing", target)
		}
	}
	if got["windows/arm64"] {
		t.Fatal("release matrix includes unsupported windows/arm64")
	}
}
