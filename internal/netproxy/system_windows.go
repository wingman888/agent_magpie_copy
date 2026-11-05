package netproxy

import (
	"os/exec"
	"strings"
)

const winInternetSettings = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// readSystem reads the Internet Options proxy values from the registry.
func readSystem() sysProxy {
	return parseWinProxy(regValue("ProxyEnable") == "0x1", regValue("ProxyServer"), regValue("ProxyOverride"))
}

// regValue returns one value of the Internet Settings key, or "".
func regValue(name string) string {
	out, err := exec.Command("reg", "query", winInternetSettings, "/v", name).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == name {
			return strings.Join(f[2:], " ") // f[1] is the type
		}
	}
	return ""
}
