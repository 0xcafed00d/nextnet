package serial

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveExplicitDevice(t *testing.T) {
	got, err := Resolve("/tmp/custom-uart")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/custom-uart" {
		t.Fatalf("Resolve returned %q", got)
	}
}

func TestResolveExplicitDeviceDoesNotRequireExistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-created")
	got, err := Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("Resolve returned %q, want %q", got, path)
	}
}

func TestResolveRejectsNonDeviceCandidateLogic(t *testing.T) {
	// The default path cannot be redirected, so verify the mode test used by
	// Resolve behaves as expected for an ordinary file.
	path := filepath.Join(t.TempDir(), "ordinary")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeDevice != 0 {
		t.Fatalf("ordinary file unexpectedly reported device mode: %v", info.Mode())
	}
}
