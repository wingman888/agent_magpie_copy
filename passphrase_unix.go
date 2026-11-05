//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
)

// readHidden reads one line from the terminal in with echo turned off. It
// restores the terminal's settings when done, and also on Ctrl-C.
func readHidden(in *os.File, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	saved, err := stty(in, "-g")
	if err != nil {
		return "", errors.New("cannot read the passphrase: the terminal does not allow turning echo off")
	}
	if _, err := stty(in, "-echo"); err != nil {
		return "", errors.New("cannot read the passphrase: the terminal does not allow turning echo off")
	}
	defer func() {
		_, _ = stty(in, saved)
		fmt.Fprintln(os.Stderr)
	}()

	done := make(chan struct{})
	defer close(done)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			_, _ = stty(in, saved)
			os.Exit(130)
		case <-done:
		}
	}()

	return readLine(in)
}

// stty runs stty against the terminal in and returns its trimmed output.
func stty(in *os.File, args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = in
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
