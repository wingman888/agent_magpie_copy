//go:build linux || darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// disableEcho turns echo off on a terminal and returns a function that
// turns it back on.
func disableEcho(f *os.File) (func(), error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, termiosGet)
	if err != nil {
		return nil, err
	}
	neu := *old
	neu.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, termiosSet, &neu); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, termiosSet, old) }, nil
}
