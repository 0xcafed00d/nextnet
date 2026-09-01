package corewatch

import (
	"os"
	"strings"
)

// Read returns the active core name with surrounding whitespace removed.
func Read(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Active deliberately uses an exact comparison. Any alternative launcher or
// unreadable/empty core file must leave the bridge inactive.
func Active(path, target string) (bool, string, error) {
	name, err := Read(path)
	if err != nil {
		return false, "", err
	}
	return name == target, name, nil
}
