package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const compatiblePriorMainTFSHA256 = "8120c94c2f3e7342101e8575fc4dd0cdf7987d478e940d0490516f014363f307"
const compatiblePriorTerraformLockSHA256 = "5e5f73fa72b5b5b453d26777834d6816d80d3fb5187c9df07c1203773f938f10"

// compatibleManagedAssetSHA256 contains narrowly reviewed historical assets
// that are functionally equivalent to the current embedded configuration. The
// embedded bytes remain canonical for new workdirs; these digests only let an
// existing deployment created by a compatible release be inspected and
// destroyed safely.
var compatibleManagedAssetSHA256 = map[string]map[string]struct{}{
	"main.tf": {
		compatiblePriorMainTFSHA256: {},
	},
	".terraform.lock.hcl": {
		compatiblePriorTerraformLockSHA256: {},
	},
}

// verifyManagedTerraformFiles keeps the CLI's documented network, IAM, and
// cleanup boundaries tied to the embedded Terraform configuration. Terraform
// automatically loads every *.tf and *.tf.json file in a directory, so silently
// accepting an extra or modified file would let an unrelated resource enter a
// reviewed plan that this tool cannot safely clean up later.
func verifyManagedTerraformFiles(dir string) error {
	return verifyManagedTerraformFilesWithCompatibility(dir, nil)
}

// verifyManagedTerraformFilesForDestroy additionally accepts narrowly reviewed
// historical assets. Compatibility is deliberately limited to cleanup: init,
// validate, and up must always use the current embedded configuration.
func verifyManagedTerraformFilesForDestroy(dir string) error {
	return verifyManagedTerraformFilesWithCompatibility(dir, compatibleManagedAssetSHA256)
}

func verifyManagedTerraformFilesWithCompatibility(dir string, compatible map[string]map[string]struct{}) error {
	expected := map[string]string{
		"main.tf":             embeddedMainTF,
		".terraform.lock.hcl": embeddedTerraformLock,
	}

	for name, want := range expected {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect managed Terraform asset %s: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("managed Terraform asset %s is not a safe regular file", name)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read managed Terraform asset %s: %w", name, err)
		}
		if !managedAssetContentMatches(got, []byte(want), compatible[name]) {
			return fmt.Errorf("managed Terraform asset %s differs from this CLI release; custom Terraform is not supported. If state exists, do not move or delete it: use the same CLI release and files that created the deployment. Otherwise, use a new empty working directory and run init", name)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("inspect Terraform configuration files: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "main.tf" && (strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json")) {
			return fmt.Errorf("unexpected Terraform configuration file %s; custom Terraform is not supported. If state exists, do not move or delete it; otherwise use a dedicated empty working directory", name)
		}
	}

	return nil
}

func managedAssetContentMatches(got, canonical []byte, compatibleSHA256 map[string]struct{}) bool {
	got = normalizeLineEndings(got)
	canonical = normalizeLineEndings(canonical)
	if bytes.Equal(got, canonical) {
		return true
	}

	digest := fmt.Sprintf("%x", sha256.Sum256(got))
	_, ok := compatibleSHA256[digest]
	return ok
}

func normalizeLineEndings(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}
