package serial

import (
	"fmt"
	"io"
	"os"
)

const defaultCandidate = "/dev/ttyS1"

// Transport is the byte stream used by the ESP emulator.
type Transport interface {
	io.Reader
	io.Writer
	io.Closer
}

// Resolve returns an explicit device unchanged or selects the verified MiSTer
// core-UART candidate when it exists. The caller still receives the open error
// so diagnostics include permissions and ownership failures.
func Resolve(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	info, err := os.Stat(defaultCandidate)
	if err != nil {
		return "", fmt.Errorf("serial auto-detection: candidate %s: %w", defaultCandidate, err)
	}
	if info.Mode()&os.ModeDevice == 0 {
		return "", fmt.Errorf("serial auto-detection: candidate %s is not a device", defaultCandidate)
	}
	return defaultCandidate, nil
}
