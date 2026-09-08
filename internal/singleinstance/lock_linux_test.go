//go:build linux

package singleinstance

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireExcludesAnotherInstanceAndRecordsPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nextnet.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantPID := strconv.Itoa(os.Getpid())
	if got := strings.TrimSpace(string(contents)); got != wantPID {
		t.Fatalf("lock PID = %q, want %q", got, wantPID)
	}

	second, err := Acquire(path)
	if second != nil {
		_ = second.Close()
		t.Fatal("second Acquire unexpectedly succeeded")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire error = %v, want ErrAlreadyRunning", err)
	}
}

func TestAcquireSucceedsAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nextnet.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRejectsEmptyPath(t *testing.T) {
	lock, err := Acquire("")
	if lock != nil {
		_ = lock.Close()
		t.Fatal("Acquire unexpectedly returned a lock")
	}
	if err == nil {
		t.Fatal("Acquire unexpectedly succeeded")
	}
}
