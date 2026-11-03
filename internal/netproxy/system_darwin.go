//go:build darwin

package netproxy

import "os/exec"

// platformSystem reads the network settings with scutil, which is how the
// system itself reports them.
func platformSystem() system {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return system{}
	}
	return parseScutil(string(out))
}
