package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed main.tf
var embeddedMainTF string

//go:embed .terraform.lock.hcl
var embeddedTerraformLock string

//go:embed gcp-free-deploy.example.json
var embeddedExampleConfig string

func ensureRuntimeAssets(dir string) error {
	assets := []struct {
		name    string
		content string
	}{
		{name: "main.tf", content: embeddedMainTF},
		{name: ".terraform.lock.hcl", content: embeddedTerraformLock},
		{name: "gcp-free-deploy.example.json", content: embeddedExampleConfig},
	}

	for _, asset := range assets {
		path := filepath.Join(dir, asset.name)
		info, err := os.Lstat(path)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("runtime asset %s is not a safe regular file", asset.name)
			}
			continue
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("inspect runtime asset %s: %w", asset.name, err)
		}

		if err := createRuntimeAsset(path, []byte(asset.content), 0o644); err != nil {
			return fmt.Errorf("create runtime asset %s: %w", asset.name, err)
		}
	}
	return nil
}

func createRuntimeAsset(path string, data []byte, perm os.FileMode) (resultErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	closed := false
	complete := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, file.Close())
		}
		if !complete {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, removeErr)
			}
		}
	}()

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	if err := file.Close(); err != nil {
		closed = true
		return fmt.Errorf("close: %w", err)
	}
	closed = true
	complete = true
	if err := syncParentDirectory(path); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
