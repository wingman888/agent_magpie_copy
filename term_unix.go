//go:build linux || darwin

package main

import (
	"bufio"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// readSecret reads one line from the terminal f with echo turned off. The
// terminal's settings are put back before it returns.
func readSecret(f *os.File) (string, error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, termiosGet)
	if err != nil {
		return "", errors.New("无法关闭回显，请改用管道提供口令")
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, termiosSet, &quiet); err != nil {
		return "", errors.New("无法关闭回显，请改用管道提供口令")
	}
	defer unix.IoctlSetTermios(fd, termiosSet, old)
	return readLine(bufio.NewReader(f))
}
