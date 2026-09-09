package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeCatalogTestDeployment(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DeployConfig{ProjectID: "sample-project", Zone: "us-central1-a", Source: "docker", DockerImage: "nginx:1.30.4", ContainerPort: 80, AllowedSourceRanges: []string{"203.0.113.10/32"}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gcp-free-deploy.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCatalogFindsDeploymentFromAnotherWorkingDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "catalog")
	t.Setenv("GCP_FREE_DEPLOY_HOME", home)
	deployment := writeCatalogTestDeployment(t, filepath.Join(t.TempDir(), "app"))
	for i := 0; i < 2; i++ {
		if err := rememberDeployment(deployment); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := discoverDeployments(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(deployment)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Dir != expected || entries[0].ProjectID != "sample-project" || entries[0].Source != "nginx:1.30.4" {
		t.Fatalf("unexpected catalog: %#v", entries)
	}
	data, err := os.ReadFile(filepath.Join(home, "deployments.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored["paths"] == nil {
		t.Fatalf("catalog should only store paths: %s", data)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(home, "deployments.json"))
		if info.Mode().Perm() != 0600 {
			t.Fatalf("permissions: %o", info.Mode().Perm())
		}
	}
}

func TestCatalogDiscoversOnlyCurrentAndDirectWizardDirectories(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", filepath.Join(t.TempDir(), "catalog"))
	workdir := writeCatalogTestDeployment(t, t.TempDir())
	child := writeCatalogTestDeployment(t, filepath.Join(workdir, "gcp-deploy-first"))
	writeCatalogTestDeployment(t, filepath.Join(child, "gcp-deploy-nested"))
	writeCatalogTestDeployment(t, filepath.Join(workdir, "unrelated"))
	if err := rememberDeployment(child); err != nil {
		t.Fatal(err)
	}
	entries, err := discoverDeployments(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want two deduplicated local deployments, got %#v", entries)
	}
	if entries[0].Dir >= entries[1].Dir {
		t.Fatal("entries must be sorted")
	}
}

func TestCatalogSkipsMissingAndInvalidConfigs(t *testing.T) {
	t.Setenv("GCP_FREE_DEPLOY_HOME", filepath.Join(t.TempDir(), "catalog"))
	dir := writeCatalogTestDeployment(t, filepath.Join(t.TempDir(), "app"))
	if err := rememberDeployment(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gcp-free-deploy.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := discoverDeployments(t.TempDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid entry retained: %#v, %v", entries, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("discovery must not delete deployment directory")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	entries, err = discoverDeployments(t.TempDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing entry retained: %#v, %v", entries, err)
	}
}

func TestCatalogRejectsSymlinksAndPreservesCorruptCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GCP_FREE_DEPLOY_HOME", home)
	dir := writeCatalogTestDeployment(t, filepath.Join(t.TempDir(), "app"))
	catalogPath := filepath.Join(home, "deployments.json")
	if err := os.WriteFile(catalogPath, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rememberDeployment(dir); err == nil {
		t.Fatal("must not overwrite corrupt catalog")
	}
	if _, err := discoverDeployments(t.TempDir()); err == nil {
		t.Fatal("must report corrupt catalog")
	}
	if err := os.Remove(catalogPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte(`{"paths":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, catalogPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := rememberDeployment(dir); err == nil {
		t.Fatal("must reject symlink catalog")
	}
	if _, err := discoverDeployments(t.TempDir()); err == nil {
		t.Fatal("must reject symlink catalog")
	}
	if err := os.Remove(catalogPath); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "gcp-free-deploy.json")
	original := filepath.Join(dir, "original.json")
	if err := os.Rename(configPath, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, configPath); err != nil {
		t.Fatal(err)
	}
	if err := rememberDeployment(dir); err == nil {
		t.Fatal("must reject symlink config")
	}
}
