package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// hideEcho turns off echo on the console behind stdin and returns the
// function that turns it back on.
func hideEcho() (func(), error) {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }, nil
}
