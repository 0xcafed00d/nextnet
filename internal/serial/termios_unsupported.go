//go:build !linux

package serial

import "fmt"

func Open(path string, baud int) (Transport, error) {
	return nil, fmt.Errorf("serial transport is supported only on Linux (device %s, baud %d)", path, baud)
}
