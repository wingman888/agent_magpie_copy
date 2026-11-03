package netproxy

import (
	"os/exec"
	"syscall"
)

const internetSettings = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// readSystem reads the Internet Options values with reg.exe, which keeps
// magpie free of a registry dependency. The console window stays hidden.
func readSystem() system {
	get := func(name string) string {
		cmd := exec.Command("reg", "query", internetSettings, "/v", name)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return regValue(string(out))
	}
	return parseWindows(get("ProxyEnable"), get("ProxyServer"), get("ProxyOverride"))
}
