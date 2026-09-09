package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// knownDeployment is local metadata only; discovering an entry never contacts GCP.
type knownDeployment struct {
	Dir       string
	ProjectID string
	Source    string
}

type deploymentCatalog struct {
	Paths []string `json:"paths"`
}

// GCP_FREE_DEPLOY_HOME overrides the catalog directory, principally for portable
// installations and tests. The catalog stores paths, never credentials or state.
func deploymentCatalogDir() (string, error) {
	if dir := os.Getenv("GCP_FREE_DEPLOY_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate deployment catalog: %w", err)
	}
	return filepath.Join(dir, "gcp-free-deploy"), nil
}

func readDeploymentCatalog(dir string) (deploymentCatalog, error) {
	var catalog deploymentCatalog
	// Reject a symlink at the catalog directory and file rather than following it.
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() {
			return catalog, fmt.Errorf("deployment catalog directory must be a real directory")
		}
	} else if !os.IsNotExist(err) {
		return catalog, err
	}
	path := filepath.Join(dir, "deployments.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return catalog, nil
	}
	if err != nil {
		return catalog, err
	}
	if !info.Mode().IsRegular() {
		return catalog, fmt.Errorf("deployment catalog must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return catalog, err
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return catalog, fmt.Errorf("read deployment catalog: %w", err)
	}
	return catalog, nil
}

func inspectKnownDeployment(dir string) (knownDeployment, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return knownDeployment{}, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return knownDeployment{}, err
	}
	if !info.IsDir() {
		return knownDeployment{}, fmt.Errorf("deployment path must be a real directory")
	}
	path := filepath.Join(absolute, "gcp-free-deploy.json")
	info, err = os.Lstat(path)
	if err != nil {
		return knownDeployment{}, err
	}
	if !info.Mode().IsRegular() {
		return knownDeployment{}, fmt.Errorf("deployment config must be a regular file")
	}
	cfg, err := LoadDeployConfig(path)
	if err != nil {
		return knownDeployment{}, err
	}
	// Resolve parent aliases (such as macOS /tmp) for stable deduplication.
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return knownDeployment{}, err
	}
	source := cfg.DockerImage
	if cfg.Source == "github" {
		source = cfg.GithubRepo
	}
	return knownDeployment{Dir: absolute, ProjectID: cfg.ProjectID, Source: source}, nil
}

// discoverDeployments looks only in the current directory, direct wizard-created
// children, and explicitly remembered paths. Stale entries do not delete data.
func discoverDeployments(workdir string) ([]knownDeployment, error) {
	dir, err := deploymentCatalogDir()
	if err != nil {
		return nil, err
	}
	catalog, err := readDeploymentCatalog(dir)
	if err != nil {
		return nil, err
	}
	paths := []string{workdir}
	for _, saved := range catalog.Paths {
		if filepath.IsAbs(saved) {
			paths = append(paths, saved)
		}
	}
	children, err := os.ReadDir(workdir)
	if err != nil {
		return nil, fmt.Errorf("discover local deployments: %w", err)
	}
	for _, child := range children {
		if child.IsDir() && strings.HasPrefix(child.Name(), "gcp-deploy-") {
			paths = append(paths, filepath.Join(workdir, child.Name()))
		}
	}
	byPath := make(map[string]knownDeployment)
	for _, path := range paths {
		deployment, err := inspectKnownDeployment(path)
		if err == nil {
			byPath[deployment.Dir] = deployment
		}
	}
	deployments := make([]knownDeployment, 0, len(byPath))
	for _, deployment := range byPath {
		deployments = append(deployments, deployment)
	}
	sort.Slice(deployments, func(i, j int) bool { return deployments[i].Dir < deployments[j].Dir })
	return deployments, nil
}

func rememberDeployment(path string) error {
	deployment, err := inspectKnownDeployment(path)
	if err != nil {
		return fmt.Errorf("remember deployment: %w", err)
	}
	dir, err := deploymentCatalogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Check before obtaining the lock, which also lives in this directory.
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("deployment catalog directory must be a real directory")
	}
	return withWorkdirLock(dir, func() error {
		catalog, err := readDeploymentCatalog(dir)
		if err != nil {
			return err
		}
		paths := make(map[string]bool)
		for _, saved := range catalog.Paths {
			paths[saved] = true
		}
		paths[deployment.Dir] = true
		catalog.Paths = make([]string, 0, len(paths))
		for saved := range paths {
			catalog.Paths = append(catalog.Paths, saved)
		}
		sort.Strings(catalog.Paths)
		data, err := json.MarshalIndent(catalog, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(dir, ".deployments-*.json")
		if err != nil {
			return err
		}
		temporary := file.Name()
		defer os.Remove(temporary)
		if _, err := file.Write(append(data, '\n')); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return os.Rename(temporary, filepath.Join(dir, "deployments.json"))
	})
}
