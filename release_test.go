package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseWorkflowExcludesUnsupportedWindowsARM64(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/release.yml")
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

func TestReleaseWorkflowProducesVersionedVerifiedArchives(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/release.yml")
	required := []string{
		"Require a stable SemVer tag",
		`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`,
		"needs: [test, native-test]",
		"go-version-file: go.mod",
		"terraform fmt -check -diff -recursive",
		"terraform init -reconfigure -lockfile=readonly -input=false -no-color",
		"terraform validate -no-color",
		"CGO_ENABLED: \"0\"",
		"go build -trimpath",
		"-X main.buildVersion=${VERSION}",
		"\"$binary\" version",
		"gcp-free-deploy $VERSION",
		"\"$binary\" init",
		".tar.gz",
		".zip",
		"sha256sum",
		"SHA256SUMS",
		"attestations: write",
		"id-token: write",
		"actions/attest-build-provenance@",
		"generate_release_notes: true",
	}
	for _, value := range required {
		if !strings.Contains(workflow, value) {
			t.Errorf("release workflow does not contain %q", value)
		}
	}
}

func TestCIWorkflowCoversGoTerraformAndWindows(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/ci.yml")
	required := []string{
		"push:",
		"pull_request:",
		"go-version-file: go.mod",
		"gofmt -l .",
		"go test -race ./...",
		"go vet ./...",
		"go build -trimpath",
		"runs-on: windows-latest",
		"go test ./...",
		"terraform fmt -check -diff -recursive",
		"terraform init -reconfigure -lockfile=readonly -input=false -no-color",
		"terraform validate -no-color",
	}
	for _, value := range required {
		if !strings.Contains(workflow, value) {
			t.Errorf("CI workflow does not contain %q", value)
		}
	}
}

func TestWorkflowsPinEveryActionToAFullCommitSHA(t *testing.T) {
	usesLine := regexp.MustCompile(`^\s*uses:\s+`)
	pinnedAction := regexp.MustCompile(`^\s*uses:\s+[^@\s]+@[0-9a-f]{40}(?:\s+#.*)?\s*$`)

	for _, path := range []string{
		".github/workflows/ci.yml",
		".github/workflows/release.yml",
	} {
		workflow := readRepositoryFile(t, path)
		found := 0
		for lineNumber, line := range strings.Split(workflow, "\n") {
			if !usesLine.MatchString(line) {
				continue
			}
			found++
			if !pinnedAction.MatchString(line) {
				t.Errorf("%s:%d action is not pinned to a full 40-character commit SHA: %s", path, lineNumber+1, strings.TrimSpace(line))
			}
		}
		if found == 0 {
			t.Errorf("%s does not contain any action uses", path)
		}
	}
}

func TestSupportedGoVersionIsSharedBySourceDocsAndWorkflows(t *testing.T) {
	const version = "1.26.8"
	goMod := readRepositoryFile(t, "go.mod")
	if !strings.Contains(goMod, "go "+version+"\n") {
		t.Errorf("go.mod does not require Go %s", version)
	}
	for _, path := range []string{"README.md", "README.ko.md", "CONTRIBUTING.md"} {
		content := readRepositoryFile(t, path)
		if !strings.Contains(content, "Go "+version) {
			t.Errorf("%s does not document Go %s", path, version)
		}
	}

	for _, path := range []string{
		".github/workflows/ci.yml",
		".github/workflows/release.yml",
	} {
		workflow := readRepositoryFile(t, path)
		setupCount := strings.Count(workflow, "actions/setup-go@")
		versionFileCount := strings.Count(workflow, "go-version-file: go.mod")
		if setupCount != versionFileCount {
			t.Errorf("%s has %d setup-go steps but %d go-version-file declarations", path, setupCount, versionFileCount)
		}
		if strings.Contains(workflow, "go-version:") {
			t.Errorf("%s hard-codes a Go version instead of reading go.mod", path)
		}
	}
}

func readRepositoryFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(normalizeLineEndings(data))
}
