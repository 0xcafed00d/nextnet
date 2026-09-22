//go:build linux

package serial

import (
	"testing"
	"unsafe"
)

func TestBaudConstant(t *testing.T) {
	if _, err := baudConstant(115200); err != nil {
		t.Fatalf("115200 should be supported: %v", err)
	}
	if _, err := baudConstant(12345); err == nil {
		t.Fatal("unexpected support for baud 12345")
	}
}

func TestResetTransition(t *testing.T) {
	tests := []struct {
		name                            string
		armed, previousCTS, currentCTS  bool
		haveCounter                     bool
		previousChanges, currentChanges uint32
		want                            bool
	}{
		{name: "idle", armed: true, previousCTS: true, currentCTS: true},
		{name: "assertion level", armed: true, previousCTS: true, currentCTS: false, want: true},
		{name: "pulse between samples", armed: true, previousCTS: true, currentCTS: true, haveCounter: true, previousChanges: 12, currentChanges: 14, want: true},
		{name: "counter unavailable", armed: true, previousCTS: true, currentCTS: true, previousChanges: 12, currentChanges: 14},
		{name: "release after opening during reset", previousCTS: false, currentCTS: true, haveCounter: true, previousChanges: 12, currentChanges: 13},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resetTransition(test.armed, test.previousCTS, test.currentCTS, test.haveCounter, test.previousChanges, test.currentChanges)
			if got != test.want {
				t.Fatalf("resetTransition() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestApplyCustomBaud(t *testing.T) {
	settings := linuxTermios2{
		Cflag:  cbaudMask | crtsctsMask,
		Ispeed: 115200,
		Ospeed: 115200,
	}
	applyCustomBaud(&settings, 230769)

	if got := settings.Cflag & cbaudMask; got != botherMask {
		t.Fatalf("baud mode bits = %#x, want BOTHER %#x", got, botherMask)
	}
	if settings.Cflag&crtsctsMask == 0 {
		t.Fatal("unrelated control flags were cleared")
	}
	if settings.Ispeed != 230769 || settings.Ospeed != 230769 {
		t.Fatalf("termios2 speeds = %d/%d, want 230769", settings.Ispeed, settings.Ospeed)
	}
	if got := unsafe.Sizeof(settings); got != 44 {
		t.Fatalf("termios2 size = %d, want 44", got)
	}
}
