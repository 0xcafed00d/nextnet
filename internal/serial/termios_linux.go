//go:build linux

package serial

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Linux does not expose these two termios masks through the frozen syscall
// package. They have these values on the linux/amd64 development target and
// linux/arm MiSTer target.
const (
	cbaudMask   uint32 = 0x0000100f
	crtsctsMask uint32 = 0x80000000
)

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
	return file, nil
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
