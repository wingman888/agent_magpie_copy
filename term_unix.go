//go:build linux || darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// hideEcho stops the terminal echoing what is typed. The returned func puts
// the settings back as they were.
func hideEcho(f *os.File) (func(), error) {
	fd := int(f.Fd())
	orig, err := unix.IoctlGetTermios(fd, termGet)
	if err != nil {
		return nil, err
	}
	saved := *orig
	orig.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, termSet, orig); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, termSet, &saved) }, nil
}
