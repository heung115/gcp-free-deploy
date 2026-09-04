package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func terraformVariablesModePassesValidation(mode os.FileMode) bool {
	return terraformVariablesModePassesValidationForOS(runtime.GOOS, mode)
}

func terraformVariablesModePassesValidationForOS(goos string, mode os.FileMode) bool {
	if goos == "windows" {
		// Windows does not expose POSIX owner/group/other permission bits through
		// FileMode. Access control is inherited from the containing directory's ACL.
		return true
	}
	return mode.Perm()&0o077 == 0
}

func preparePlanDestination(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect plan destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("plan destination is not a safe regular file")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale plan file: %w", err)
	}
	return nil
}

// writeFileSafely stages, flushes, and replaces a file. The final rename is
// atomic on platforms that provide atomic same-directory replacement; Windows
// receives the strongest replacement semantics exposed by os.Rename.
func writeFileSafely(path string, data []byte, perm os.FileMode) error {
	return writeFileSafelyWithRename(path, data, perm, os.Rename)
}

func writeFileSafelyWithRename(path string, data []byte, perm os.FileMode, rename func(string, string) error) (resultErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	renamed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close temporary file during cleanup: %w", closeErr))
			}
		}
		if !renamed {
			if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary file during cleanup: %w", removeErr))
			}
		}
	}()

	if err := temporary.Chmod(perm); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	closeErr := temporary.Close()
	closed = true
	if closeErr != nil {
		return fmt.Errorf("close temporary file: %w", closeErr)
	}
	if err := rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace destination file: %w", err)
	}
	renamed = true
	if err := syncParentDirectory(path); err != nil {
		return fmt.Errorf("sync destination directory: %w", err)
	}
	return nil
}
