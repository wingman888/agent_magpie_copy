//go:build !linux && !darwin && !windows

package main

import (
	"os"
	"os/exec"
)

// hideEcho turns echo off with stty, the only portable way left on other
// systems. If stty is missing the error is returned and nothing is read.
func hideEcho(f *os.File) (func(), error) {
	run := func(arg string) error {
		cmd := exec.Command("stty", arg)
		cmd.Stdin = f
		return cmd.Run()
	}
	if err := run("-echo"); err != nil {
		return nil, err
	}
	return func() { _ = run("echo") }, nil
}
