//go:build !linux

package singleinstance

import "fmt"

type Lock struct{}

func Acquire(path string) (*Lock, error) {
	return nil, fmt.Errorf("single-instance locking is supported only on Linux (path %s)", path)
}

func (l *Lock) Close() error {
	return nil
}
