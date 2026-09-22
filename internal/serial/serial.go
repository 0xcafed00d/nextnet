package serial

import (
	"context"
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

// BaudSetter changes a live transport's baud rate after queued output has
// drained. Linux implements this with termios2 so non-standard rates such as
// the Spectrum Next's 230769 baud are supported.
type BaudSetter interface {
	SetBaud(int) error
}

// ResetWatcher is implemented by transports that can observe the ZXNext
// peripheral-reset signal. The patched MiSTer core routes that signal through
// UART_RTS to the HPS UART's CTS input. A nil result means a reset edge was
// detected; context cancellation stops the monitor.
type ResetWatcher interface {
	WaitForReset(context.Context) error
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
