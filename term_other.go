//go:build !linux && !darwin && !windows

package main

import "os"

// fdIsTerminal falls back to the character-device check on platforms without
// a terminal ioctl wired up here.
func fdIsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
