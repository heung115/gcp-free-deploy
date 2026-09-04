package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkdirLockRejectsConcurrentHolderAndReleasesCleanly(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireWorkdirLock(dir)
	if err != nil {
		t.Fatalf("first acquireWorkdirLock() returned an error: %v", err)
	}

	second, err := acquireWorkdirLock(dir)
	if second != nil || !errors.Is(err, errWorkdirLocked) {
		if second != nil {
			second.Release()
		}
		first.Release()
		t.Fatalf("second acquireWorkdirLock() = %#v, %v; want nil, errWorkdirLocked", second, err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release() returned an error: %v", err)
	}
	reacquired, err := acquireWorkdirLock(dir)
	if err != nil {
		t.Fatalf("acquireWorkdirLock() after release returned an error: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatalf("second Release() returned an error: %v", err)
	}
}

func TestInitRefusesToRunWhileTheWorkingDirectoryIsLocked(t *testing.T) {
	dir := t.TempDir()
	held, err := acquireWorkdirLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	err = runCLI(context.Background(), []string{"init"}, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}, &recordingRunner{}, dir)
	if !errors.Is(err, errWorkdirLocked) {
		t.Fatalf("runCLI(init) error = %v, want errWorkdirLocked", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "main.tf")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("init changed the working directory while locked: %v", statErr)
	}
}
