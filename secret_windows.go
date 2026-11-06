//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// readHiddenLine reads one line from the console f with echo switched off.
// The console mode is put back before it returns.
func readHiddenLine(f *os.File) (string, error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return "", err
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", err
	}
	defer windows.SetConsoleMode(h, mode)
	return readLineFrom(f)
}
