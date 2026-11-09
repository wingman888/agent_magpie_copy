package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// hideEcho turns off echo on the console behind stdin and returns a function
// that turns it back on.
func hideEcho() (func(), error) {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, errors.New("无法关闭控制台回显，未执行")
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return nil, errors.New("无法关闭控制台回显，未执行")
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }, nil
}
