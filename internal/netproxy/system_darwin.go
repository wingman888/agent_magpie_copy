package netproxy

import "os/exec"

// readSystem asks macOS for the network proxy settings (System Settings > Network).
func readSystem() sysProxy {
	out, err := exec.Command("/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return sysProxy{}
	}
	return parseScutil(out)
}
