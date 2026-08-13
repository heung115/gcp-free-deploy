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
				return fmt.Errorf("runtime asset %s가 안전한 일반 파일이 아닙니다", asset.name)
			}
			continue
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("runtime asset %s 확인: %w", asset.name, err)
		}

		if err := os.WriteFile(path, []byte(asset.content), 0o644); err != nil {
			return fmt.Errorf("runtime asset %s 생성: %w", asset.name, err)
		}
	}
	return nil
}
