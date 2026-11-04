//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// hideEcho stops the console echoing what is typed. The returned func puts
// the console mode back as it was.
func hideEcho(f *os.File) (func(), error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }, nil
}
