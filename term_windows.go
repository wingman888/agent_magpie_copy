package main

import (
	"os"
	"syscall"
	"unsafe"
)

var procGetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleMode")

// fdIsTerminal reports whether f is a console: GetConsoleMode succeeds only on
// a console handle, not on NUL, pipes or files.
func fdIsTerminal(f *os.File) bool {
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	return r != 0
}
