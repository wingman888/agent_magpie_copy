//go:build nogui

package main

import "errors"

const hasGUI = false

func runGUI(bool) error { return errors.New("this build has no GUI; run `magpie tui`") }

// openLink: without the app, a magpie: link goes through the CLI flow.
func openLink(link string) error { return importCmd([]string{link}) }
