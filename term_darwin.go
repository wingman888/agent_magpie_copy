//go:build darwin

package main

import "golang.org/x/sys/unix"

// termGet and termSet are the ioctls that read and write a terminal's settings.
const (
	termGet uint = unix.TIOCGETA
	termSet uint = unix.TIOCSETA
)
