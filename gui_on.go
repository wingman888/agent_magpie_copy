//go:build !nogui

package main

import "github.com/yetone/magpie/internal/gui"

const hasGUI = true

func runGUI(showMain bool) error {
	registerProtocol()
	return gui.Run(version, showMain)
}

// openLink hands a magpie: link to the app: the running one when there is
// one (the second launch forwards it), else this process becomes the app.
func openLink(link string) error {
	registerProtocol()
	gui.SetLink(link)
	return gui.Run(version, false)
}
