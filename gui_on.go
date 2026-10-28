//go:build !nogui

package main

import "github.com/yetone/dial/internal/gui"

const hasGUI = true

func runGUI(showMain bool) error { return gui.Run(version, showMain) }
