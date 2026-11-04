//go:build linux

package main

import "golang.org/x/sys/unix"

// termGet and termSet are the ioctls that read and write a terminal's settings.
const (
	termGet uint = unix.TCGETS
	termSet uint = unix.TCSETS
)
