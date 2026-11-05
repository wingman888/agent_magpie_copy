//go:build !linux && !darwin && !windows

package main

import (
	"errors"
	"os"
)

// readSecret is unavailable here: without a way to switch echo off, a
// password typed at the terminal would show on screen.
func readSecret(f *os.File) (string, error) {
	return "", errors.New("此系统不支持隐藏输入，请改用管道提供口令")
}
