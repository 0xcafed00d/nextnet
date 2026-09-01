package corewatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActiveRequiresExactTrimmedCoreName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CORENAME")

	for _, tc := range []struct {
		contents string
		want     bool
	}{
		{contents: "ZXNext\n", want: true},
		{contents: " MENU \n", want: false},
		{contents: "AlternativeLauncher\n", want: false},
		{contents: "ZXNext-dev\n", want: false},
		{contents: "\n", want: false},
	} {
		if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
			t.Fatal(err)
		}
		got, _, err := Active(path, "ZXNext")
		if err != nil {
			t.Fatalf("Active(%q): %v", tc.contents, err)
		}
		if got != tc.want {
			t.Errorf("Active(%q) = %v, want %v", tc.contents, got, tc.want)
		}
	}
}

func TestActiveTreatsMissingFileAsInactive(t *testing.T) {
	active, name, err := Active(filepath.Join(t.TempDir(), "missing"), "ZXNext")
	if err == nil {
		t.Fatal("Active returned a nil error for a missing file")
	}
	if active || name != "" {
		t.Fatalf("Active returned active=%v name=%q for a missing file", active, name)
	}
}
