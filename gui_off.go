//go:build nogui

package main

import "errors"

const hasGUI = false

func runGUI(bool, string) error { return errors.New("this build has no GUI; run `magpie tui`") }
