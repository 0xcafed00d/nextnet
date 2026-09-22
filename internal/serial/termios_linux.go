//go:build linux

package serial

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Linux does not expose these two termios masks through the frozen syscall
// package. They have these values on the linux/amd64 development target and
// linux/arm MiSTer target.
const (
	cbaudMask   uint32 = 0x0000100f
	crtsctsMask uint32 = 0x80000000
	botherMask  uint32 = 0x00001000

	tcgets2  uintptr = 0x802c542a
	tcsetsw2 uintptr = 0x402c542c

	// These generic Linux TTY ioctl values are shared by MiSTer's ARM Linux
	// target and the amd64 development target. TIOCGICOUNT is used in addition
	// to level polling so a complete CTS pulse between samples is not missed.
	tiocmget    uintptr = 0x5415
	tiocgicount uintptr = 0x545d
	tiocmCTS    int32   = 0x0020

	resetPollInterval = 2 * time.Millisecond
)

type linuxTransport struct {
	*os.File
}

// linuxTermios2 matches the generic Linux struct termios2 used by both the
// amd64 development host and MiSTer's 32-bit ARM kernel.
type linuxTermios2 struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Line   uint8
	Cc     [19]uint8
	Ispeed uint32
	Ospeed uint32
}

// Open configures a Linux TTY as raw 8N1 with no software or hardware flow
// control. O_NONBLOCK avoids waiting for carrier during open and lets Go's file
// poller interrupt a read promptly when the port is closed on a core change.
func Open(path string, baud int) (Transport, error) {
	baudBits, err := baudConstant(baud)
	if err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open serial device %s: %w", path, err)
	}

	if err := configure(file.Fd(), baudBits); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("configure serial device %s at %d baud: %w", path, baud, err)
	}
	return &linuxTransport{File: file}, nil
}

// SetBaud waits for bytes already handed to the UART driver to leave at the
// old rate, then selects an exact requested rate with Linux's BOTHER support.
// The UART driver may round this to the closest rate its clock divisor can
// produce, as physical UARTs normally do.
func (p *linuxTransport) SetBaud(baud int) error {
	if baud <= 0 {
		return fmt.Errorf("baud rate must be positive")
	}
	if uint64(baud) > uint64(^uint32(0)) {
		return fmt.Errorf("baud rate %d is too large", baud)
	}

	var settings linuxTermios2
	if err := ioctl(p.Fd(), tcgets2, uintptr(unsafe.Pointer(&settings))); err != nil {
		return fmt.Errorf("read termios2: %w", err)
	}
	applyCustomBaud(&settings, uint32(baud))
	// TCSETSW2 waits for queued output to drain before atomically applying the
	// new settings, so the command response leaves at the previous baud.
	if err := ioctl(p.Fd(), tcsetsw2, uintptr(unsafe.Pointer(&settings))); err != nil {
		return fmt.Errorf("set termios2 baud %d after drain: %w", baud, err)
	}
	return nil
}

func applyCustomBaud(settings *linuxTermios2, baud uint32) {
	settings.Cflag &^= cbaudMask
	settings.Cflag |= botherMask
	settings.Ispeed = baud
	settings.Ospeed = baud
}

// WaitForReset observes the active-low HPS CTS state produced by the patched
// ZXNext core. The core drives UART_RTS low while idle, which Linux reports as
// asserted CTS, and drives it high while NextReg $02 bit 7 requests an ESP
// reset. The CTS transition counter catches pulses shorter than one polling
// interval when the UART driver supports TIOCGICOUNT.
func (p *linuxTransport) WaitForReset(ctx context.Context) error {
	previousCTS, err := ctsAsserted(p.Fd())
	if err != nil {
		return fmt.Errorf("read initial CTS state: %w", err)
	}
	previousChanges, counterErr := ctsChangeCount(p.Fd())
	haveChangeCounter := counterErr == nil
	armed := previousCTS

	ticker := time.NewTicker(resetPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		currentCTS, err := ctsAsserted(p.Fd())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read CTS state: %w", err)
		}

		currentChanges := previousChanges
		if haveChangeCounter {
			var counterErr error
			currentChanges, counterErr = ctsChangeCount(p.Fd())
			if counterErr != nil {
				haveChangeCounter = false
			}
		}

		if resetTransition(armed, previousCTS, currentCTS, haveChangeCounter, previousChanges, currentChanges) {
			return nil
		}
		if currentCTS {
			armed = true
		}
		previousCTS = currentCTS
		previousChanges = currentChanges
	}
}

func ctsAsserted(fd uintptr) (bool, error) {
	var modemBits int32
	if err := ioctl(fd, tiocmget, uintptr(unsafe.Pointer(&modemBits))); err != nil {
		return false, err
	}
	return modemBits&tiocmCTS != 0, nil
}

func ctsChangeCount(fd uintptr) (uint32, error) {
	// Linux struct serial_icounter_struct contains twenty 32-bit integers;
	// CTS is its first member.
	var counters [20]int32
	if err := ioctl(fd, tiocgicount, uintptr(unsafe.Pointer(&counters[0]))); err != nil {
		return 0, err
	}
	return uint32(counters[0]), nil
}

func resetTransition(armed, previousCTS, currentCTS, haveChangeCounter bool, previousChanges, currentChanges uint32) bool {
	if !armed {
		return false
	}
	if previousCTS && !currentCTS {
		return true
	}
	return haveChangeCounter && currentChanges != previousChanges
}

func configure(fd uintptr, baud uint32) error {
	var settings syscall.Termios
	if err := ioctl(fd, syscall.TCGETS, uintptr(unsafe.Pointer(&settings))); err != nil {
		return err
	}

	settings.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK |
		syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL |
		syscall.IXON | syscall.IXOFF | syscall.IXANY | syscall.INPCK
	settings.Oflag &^= syscall.OPOST
	settings.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON |
		syscall.ISIG | syscall.IEXTEN
	settings.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.PARODD |
		syscall.CSTOPB | cbaudMask | crtsctsMask
	settings.Cflag |= syscall.CS8 | syscall.CREAD | syscall.CLOCAL | baud
	settings.Ispeed = baud
	settings.Ospeed = baud
	settings.Cc[syscall.VMIN] = 1
	settings.Cc[syscall.VTIME] = 0

	return ioctl(fd, syscall.TCSETS, uintptr(unsafe.Pointer(&settings)))
}

func ioctl(fd, request, argument uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, argument)
	if errno != 0 {
		return errno
	}
	return nil
}

func baudConstant(baud int) (uint32, error) {
	values := map[int]uint32{
		1200:   syscall.B1200,
		2400:   syscall.B2400,
		4800:   syscall.B4800,
		9600:   syscall.B9600,
		19200:  syscall.B19200,
		38400:  syscall.B38400,
		57600:  syscall.B57600,
		115200: syscall.B115200,
		230400: syscall.B230400,
		460800: syscall.B460800,
		921600: syscall.B921600,
	}
	value, ok := values[baud]
	if !ok {
		return 0, fmt.Errorf("unsupported baud rate %d", baud)
	}
	return value, nil
}
