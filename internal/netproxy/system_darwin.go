package netproxy

import "os/exec"

// readSystem reads the network settings from scutil, which is what the
// System Settings network pane writes.
func readSystem() system {
	out, err := exec.Command("/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return system{}
	}
	return parseScutil(string(out))
}
