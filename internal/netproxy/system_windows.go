package netproxy

import (
	"os/exec"
)

const internetSettings = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func readOS() sysProxy {
	query := func(name string) string {
		out, err := exec.Command("reg", "query", internetSettings, "/v", name).Output()
		if err != nil {
			return ""
		}
		_, v, _ := regValue(string(out), name)
		return v
	}
	return parseWindows(query("ProxyEnable"), query("ProxyServer"), query("ProxyOverride"))
}
