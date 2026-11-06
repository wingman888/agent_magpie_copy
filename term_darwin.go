//go:build darwin

package main

import "golang.org/x/sys/unix"

const termGet, termSet = unix.TIOCGETA, unix.TIOCSETA
