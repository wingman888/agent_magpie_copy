package netproxy

import "os/exec"

// system reads what System Settings → Network → Proxies says, through
// scutil (the same dictionary CFNetwork uses).
func system() Proxy {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return Proxy{}
	}
	return parseScutil(string(out))
}
