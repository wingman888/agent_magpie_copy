//go:build windows

package netproxy

import (
	"context"
	"os/exec"
	"time"
)

const internetSettingsKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// readSystem reads the manual proxy from the Internet Settings registry key,
// using `reg query`. A failure means there is no system proxy.
func readSystem() sysConfig {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "reg", "query", internetSettingsKey).Output()
	if err != nil {
		return sysConfig{}
	}
	s := string(out)
	return parseWindowsProxy(
		parseRegValue(s, "ProxyEnable"),
		parseRegValue(s, "ProxyServer"),
		parseRegValue(s, "ProxyOverride"),
	)
}
