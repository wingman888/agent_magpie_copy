//go:build windows

package main

import (
	"bufio"
	"os"

	"golang.org/x/sys/windows"
)

// readSecret reads one line from the console f with echo turned off. The
// console mode is put back before it returns.
func readSecret(f *os.File) (string, error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return "", err
	}
	const enableEchoInput = 0x0004
	if err := windows.SetConsoleMode(h, mode&^enableEchoInput); err != nil {
		return "", err
	}
	defer windows.SetConsoleMode(h, mode)
	return readLine(bufio.NewReader(f))
}
