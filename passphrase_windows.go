//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// readHidden reads one line from the console in with echo turned off. The
// console mode is restored when done.
func readHidden(in *os.File, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	h := windows.Handle(in.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return "", errors.New("cannot read the passphrase: not a console")
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", errors.New("cannot read the passphrase: the console does not allow turning echo off")
	}
	defer func() {
		_ = windows.SetConsoleMode(h, mode)
		fmt.Fprintln(os.Stderr)
	}()
	return readLine(in)
}
