//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkdirLockRefusesSymlinkWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, workdirLockName)); err != nil {
		t.Fatal(err)
	}

	lock, err := acquireWorkdirLock(dir)
	if lock != nil {
		lock.Release()
		t.Fatal("acquireWorkdirLock() followed a symlink")
	}
	if err == nil {
		t.Fatal("acquireWorkdirLock() returned nil error for a symlink")
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("target permissions changed to %o, want 644", got)
	}
}
