//go:build linux

package main

import "golang.org/x/sys/unix"

const termGet, termSet = unix.TCGETS, unix.TCSETS
