//go:build !linux && !darwin && !windows

package main

import "os"

// Without a termios or console API here, a terminal is read like a pipe:
// the passphrase is taken from the first line and may be echoed.
func isTerminal(f *os.File) bool { return false }

func echoOff(f *os.File) (restore func(), ok bool) { return func() {}, false }
