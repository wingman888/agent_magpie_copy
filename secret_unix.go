//go:build linux || darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// readHiddenLine reads one line from the terminal f with echo switched off.
// The terminal's settings are put back before it returns.
func readHiddenLine(f *os.File) (string, error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, getTermiosReq)
	if err != nil {
		return "", err
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, setTermiosReq, &quiet); err != nil {
		return "", err
	}
	defer unix.IoctlSetTermios(fd, setTermiosReq, old)
	return readLineFrom(f)
}
