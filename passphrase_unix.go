//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strings"
)

// hideEcho turns off echo on the terminal behind stdin and returns the
// function that turns it back on.
func hideEcho() (func(), error) {
	saved, err := stty("-g")
	if err != nil {
		return nil, err
	}
	if _, err := stty("-echo"); err != nil {
		return nil, err
	}
	return func() { _, _ = stty(saved) }, nil
}

func stty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
