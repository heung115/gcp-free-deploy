package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureRuntimeAssetsMaterializesStandaloneFiles(t *testing.T) {
	dir := t.TempDir()

	if err := ensureRuntimeAssets(dir); err != nil {
		t.Fatalf("ensureRuntimeAssets() returned an error: %v", err)
	}

	for name, want := range map[string]string{
		"main.tf":                      embeddedMainTF,
		".terraform.lock.hcl":          embeddedTerraformLock,
		"gcp-free-deploy.example.json": embeddedExampleConfig,
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != want {
			t.Fatalf("%s differs from the embedded asset", name)
		}
	}
}

func TestEnsureRuntimeAssetsPreservesExistingTerraform(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	const existing = "# user-managed Terraform\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureRuntimeAssets(dir); err != nil {
		t.Fatalf("ensureRuntimeAssets() returned an error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existing {
		t.Fatalf("existing main.tf was overwritten: %q", data)
	}
}

func TestCreateRuntimeAssetNeverOverwritesAnExistingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.tf")
	if err := os.WriteFile(path, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := createRuntimeAsset(path, []byte("replace\n"), 0o644); err == nil {
		t.Fatal("createRuntimeAsset() overwrote an existing path")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep\n" {
		t.Fatalf("existing file changed to %q", got)
	}
}

func TestInitCommandPreparesAStandaloneDirectory(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	runner := &recordingRunner{}

	if err := runCLI(context.Background(), []string{"init"}, &bytes.Buffer{}, &out, &bytes.Buffer{}, runner, dir); err != nil {
		t.Fatalf("runCLI(init) returned an error: %v", err)
	}
	for _, name := range []string{"main.tf", ".terraform.lock.hcl", "gcp-free-deploy.example.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("init did not create %s: %v", name, err)
		}
	}
	if len(runner.commands) != 0 {
		t.Fatalf("init ran external commands: %#v", runner.commands)
	}
}

func TestInitRejectsModifiedManagedTerraformWithoutOverwritingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	const existing = "# custom Terraform must never be run implicitly\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runCLI(context.Background(), []string{"init"}, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}, &recordingRunner{}, dir)
	if err == nil || !strings.Contains(err.Error(), "differs from this CLI release") {
		t.Fatalf("runCLI(init) error = %v, want managed Terraform mismatch", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != existing {
		t.Fatalf("init overwrote the existing main.tf: %q", data)
	}
}
