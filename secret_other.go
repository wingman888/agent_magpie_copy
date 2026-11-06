//go:build !linux && !darwin && !windows

package main

import (
	"errors"
	"os"
)

// readHiddenLine refuses on systems where magpie cannot switch echo off, so a
// passphrase is never shown on screen. Piping it in works everywhere.
func readHiddenLine(f *os.File) (string, error) {
	return "", errors.New("this system cannot hide typed input; pipe the passphrase instead, e.g. echo 'pass' | magpie backup")
}
