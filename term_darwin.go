package main

import (
	"os"
	"syscall"
	"unsafe"
)

// fdIsTerminal reports whether f is a terminal: the TIOCGETA ioctl succeeds
// only on a tty. /dev/null and pipes fail with ENOTTY, so they are not terminals.
func fdIsTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
