//go:build !linux && !darwin && !windows

package main

import (
	"errors"
	"os"
)

// disableEcho is not available on this platform: the passphrase is read
// with echo on.
func disableEcho(f *os.File) (func(), error) {
	return nil, errors.New("echo control not supported")
}
