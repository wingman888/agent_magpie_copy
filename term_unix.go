//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

// hideEcho turns off echo on the terminal behind stdin and returns a function
// that turns it back on. It uses stty, which every unix has.
func hideEcho() (func(), error) {
	errNoStty := errors.New("无法关闭终端回显（需要 stty），未执行")
	state, err := stty(os.Stdin, "-g")
	if err != nil {
		return nil, errNoStty
	}
	state = strings.TrimSpace(state)
	if _, err := stty(os.Stdin, "-echo"); err != nil {
		return nil, errNoStty
	}
	return func() { _, _ = stty(os.Stdin, state) }, nil
}

func stty(in *os.File, args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = in
	out, err := cmd.Output()
	return string(out), err
}
