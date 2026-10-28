package netproxy

import "os/exec"

func platformRead() Proxy {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return Proxy{}
	}
	return parseScutil(string(out))
}
