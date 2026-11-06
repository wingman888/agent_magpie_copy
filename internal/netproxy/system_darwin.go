package netproxy

import "os/exec"

func readOSL3x() sysProxyL3x {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return sysProxyL3x{}
	}
	return parseScutilL3x(string(out))
}
