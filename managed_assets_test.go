package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyManagedTerraformFilesAcceptsEmbeddedAssets(t *testing.T) {
	dir := t.TempDir()
	if err := ensureRuntimeAssets(dir); err != nil {
		t.Fatal(err)
	}
	if err := verifyManagedTerraformFiles(dir); err != nil {
		t.Fatalf("verifyManagedTerraformFiles() rejected embedded assets: %v", err)
	}
}

func TestVerifyManagedTerraformFilesRejectsModifiedMainTF(t *testing.T) {
	dir := t.TempDir()
	if err := ensureRuntimeAssets(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte("resource \"google_compute_instance\" \"other\" {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := verifyManagedTerraformFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "differs from this CLI release") {
		t.Fatalf("verifyManagedTerraformFiles() error = %v, want managed-asset mismatch", err)
	}
}

func TestVerifyManagedTerraformFilesRejectsAdditionalTerraform(t *testing.T) {
	dir := t.TempDir()
	if err := ensureRuntimeAssets(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.tf"), []byte("resource \"google_compute_disk\" \"other\" {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := verifyManagedTerraformFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "unexpected Terraform configuration file extra.tf") {
		t.Fatalf("verifyManagedTerraformFiles() error = %v, want extra-file rejection", err)
	}
}

func TestVerifyManagedTerraformFilesRejectsAdditionalTerraformInBracketedWorkdir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deploy[1]")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimeAssets(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.tf"), []byte("terraform {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := verifyManagedTerraformFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "unexpected Terraform configuration file extra.tf") {
		t.Fatalf("verifyManagedTerraformFiles() error = %v, want extra-file rejection", err)
	}
}

func TestVerifyManagedTerraformFilesAcceptsWindowsLineEndings(t *testing.T) {
	dir := t.TempDir()
	if err := ensureRuntimeAssets(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.tf", ".terraform.lock.hcl"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.ReplaceAll(string(data), "\n", "\r\n"))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := verifyManagedTerraformFiles(dir); err != nil {
		t.Fatalf("verifyManagedTerraformFiles() rejected CRLF-only changes: %v", err)
	}
}

func TestManagedAssetContentMatchesReviewedCompatibleDigest(t *testing.T) {
	prior := []byte("reviewed compatible asset\r\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(normalizeLineEndings(prior)))
	allowed := map[string]struct{}{digest: {}}

	if !managedAssetContentMatches(prior, []byte("current canonical asset\n"), allowed) {
		t.Fatal("managedAssetContentMatches() rejected a reviewed compatible digest")
	}
	if managedAssetContentMatches([]byte("unreviewed change\n"), []byte("current canonical asset\n"), allowed) {
		t.Fatal("managedAssetContentMatches() accepted an unreviewed change")
	}
}

func TestPriorMainTFDigestIsExplicitlyAllowlisted(t *testing.T) {
	if _, ok := compatibleManagedAssetSHA256["main.tf"][compatiblePriorMainTFSHA256]; !ok {
		t.Fatalf("prior compatible main.tf digest %s is not allowlisted", compatiblePriorMainTFSHA256)
	}
	if len(compatibleManagedAssetSHA256["main.tf"]) != 1 {
		t.Fatalf("main.tf compatible digest count = %d, want exactly 1 reviewed historical asset", len(compatibleManagedAssetSHA256["main.tf"]))
	}
}

func TestHistoricalCompatibilityIsLimitedToDestroy(t *testing.T) {
	dir := t.TempDir()
	if err := ensureRuntimeAssets(dir); err != nil {
		t.Fatal(err)
	}
	historical := []byte("reviewed historical main.tf\n")
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), historical, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(historical))
	previous := compatibleManagedAssetSHA256
	compatibleManagedAssetSHA256 = map[string]map[string]struct{}{
		"main.tf": {digest: {}},
	}
	defer func() { compatibleManagedAssetSHA256 = previous }()

	if err := verifyManagedTerraformFiles(dir); err == nil {
		t.Fatal("normal verification accepted a historical asset")
	}
	if err := verifyManagedTerraformFilesForDestroy(dir); err != nil {
		t.Fatalf("destroy verification rejected a reviewed historical asset: %v", err)
	}

	err := runCLI(nil, []string{"init"}, strings.NewReader(""), os.Stdout, os.Stderr, &recordingRunner{}, dir)
	if err == nil {
		t.Fatal("init accepted a historical asset")
	}
}
