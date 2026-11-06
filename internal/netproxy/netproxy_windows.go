package netproxy

import (
	"os/exec"
	"strings"
)

const internetSettings = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func readSystemProxy() sysProxy {
	enable := regValue("ProxyEnable") == "0x1"
	return parseWindowsProxy(enable, regValue("ProxyServer"), regValue("ProxyOverride"))
}

// regValue returns the data of one value of the Internet Settings key, or "".
func regValue(name string) string {
	out, err := exec.Command("reg", "query", internetSettings, "/v", name).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// "    ProxyServer    REG_SZ    127.0.0.1:7890": the data follows the type.
		for i := 1; i < len(f); i++ {
			if strings.HasPrefix(f[i], "REG_") {
				return strings.Join(f[i+1:], " ")
			}
		}
	}
	return ""
}
