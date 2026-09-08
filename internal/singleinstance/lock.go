package singleinstance

import "errors"

// ErrAlreadyRunning is returned when another process holds the instance lock.
var ErrAlreadyRunning = errors.New("another nextnet instance is already running")
