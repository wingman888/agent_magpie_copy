//go:build windows

package netproxy

import (
	"os/exec"
	"strings"
	"unicode"
)

const inetKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// platformSystem reads the Internet Settings of the current user. The
// standard library has no registry API, so this asks reg.exe, which ships
// with every Windows.
func platformSystem() system {
	enabled := regValue("ProxyEnable") == "0x1"
	return parseWindows(enabled, regValue("ProxyServer"), regValue("ProxyOverride"))
}

// regValue returns the data of one value, or "" when it is missing. A
// `reg query` line looks like "    ProxyServer    REG_SZ    host:port".
func regValue(name string) string {
	out, err := exec.Command("reg", "query", inetKey, "/v", name).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, name) {
			continue
		}
		rest := strings.TrimSpace(t[len(name):])
		if rest == "" || !unicode.IsSpace(rune(t[len(name)])) {
			continue
		}
		i := strings.IndexFunc(rest, unicode.IsSpace)
		if i < 0 {
			return ""
		}
		return strings.TrimSpace(rest[i:])
	}
	return ""
}
