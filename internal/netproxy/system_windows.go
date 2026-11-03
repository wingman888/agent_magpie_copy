package netproxy

import (
	"os/exec"
	"strings"
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

// regValue returns the data of the value line in `reg query` output:
//
//	ProxyServer    REG_SZ    127.0.0.1:7890
func regValue(out string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[1], "REG_") {
			continue
		}
		i := strings.Index(line, f[1])
		return strings.TrimSpace(line[i+len(f[1]):])
	}
	return ""
}
