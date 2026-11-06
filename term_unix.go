//go:build linux || darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), termGet)
	return err == nil
}

// echoOff turns echo off on the terminal f. The returned func restores it;
// ok is false when f is not a terminal or echo could not be changed.
func echoOff(f *os.File) (restore func(), ok bool) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, termGet)
	if err != nil {
		return func() {}, false
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, termSet, &quiet); err != nil {
		return func() {}, false
	}
	return func() { _ = unix.IoctlSetTermios(fd, termSet, old) }, true
}
