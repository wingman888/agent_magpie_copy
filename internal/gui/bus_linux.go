package gui

import "github.com/godbus/dbus/v5"

// sessionBus reports whether a D-Bus session bus is reachable; Wails'
// single-instance lock needs one and gives up on the whole app without it.
func sessionBus() bool {
	_, err := dbus.SessionBus()
	return err == nil
}
