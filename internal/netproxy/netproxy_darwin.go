package netproxy

import "os/exec"

func readSystemProxy() sysProxy {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return sysProxy{}
	}
	return parseScutil(string(out))
}
