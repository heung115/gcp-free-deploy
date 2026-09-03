package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const workdirLockName = ".gcp-free-deploy.lock"

var errWorkdirLocked = errors.New("another gcp-free-deploy command is already running in this working directory")

type workdirLock struct {
	file *os.File
}

func acquireWorkdirLock(workdir string) (*workdirLock, error) {
	path := filepath.Join(workdir, workdirLockName)
	file, err := openExclusiveLock(path)
	if err != nil {
		if errors.Is(err, errWorkdirLocked) {
			return nil, errWorkdirLocked
		}
		return nil, fmt.Errorf("acquire working-directory lock: %w", err)
	}
	return &workdirLock{file: file}, nil
}

func (l *workdirLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := closeExclusiveLock(l.file)
	l.file = nil
	if err != nil {
		return fmt.Errorf("release working-directory lock: %w", err)
	}
	return nil
}

func withWorkdirLock(workdir string, action func() error) (err error) {
	lock, err := acquireWorkdirLock(workdir)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()
	return action()
}
