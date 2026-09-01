//go:build linux

package serial

import "testing"

func TestBaudConstant(t *testing.T) {
	if _, err := baudConstant(115200); err != nil {
		t.Fatalf("115200 should be supported: %v", err)
	}
	if _, err := baudConstant(12345); err == nil {
		t.Fatal("unexpected support for baud 12345")
	}
}
