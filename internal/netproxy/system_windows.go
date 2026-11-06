package netproxy

import (
	"os/exec"
)

const internetSettingsL3x = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func readOSL3x() sysProxyL3x {
	query := func(name string) string {
		out, err := exec.Command("reg", "query", internetSettingsL3x, "/v", name).Output()
		if err != nil {
			return ""
		}
		_, v, _ := regValueL3x(string(out), name)
		return v
	}
	return parseWindowsL3x(query("ProxyEnable"), query("ProxyServer"), query("ProxyOverride"))
}
