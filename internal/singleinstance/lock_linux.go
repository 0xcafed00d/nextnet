//go:build linux

package singleinstance

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Lock is an advisory process lock held for as long as its file remains open.
type Lock struct {
	file *os.File
}

// Acquire takes a non-blocking exclusive lock and records the owning PID. The
// lock file may outlive the process; ownership is determined by flock, not by
// the presence or contents of the file.
func Acquire(path string) (*Lock, error) {
	if path == "" {
		return nil, fmt.Errorf("instance lock path must not be empty")
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open instance lock %s: %w", path, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w (%s)", ErrAlreadyRunning, path)
		}
		return nil, fmt.Errorf("lock instance file %s: %w", path, err)
	}

	if err := writePID(file); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("record PID in instance lock %s: %w", path, err)
	}

	return &Lock{file: file}, nil
}

func writePID(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	_, err := fmt.Fprintf(file, "%d\n", os.Getpid())
	return err
}

// Close releases the advisory lock. The file is deliberately left in place so
// a stale pathname can never be mistaken for lock ownership.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
