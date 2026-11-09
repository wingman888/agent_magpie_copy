package netproxy

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// createNoWindow is CREATE_NO_WINDOW, so the query does not flash a console.
const createNoWindow = 0x08000000

// platformSystemProxy reads the manual proxy under "Internet Options" from the
// current user's registry key with `reg query`. Any failure means there is no
// system proxy.
func platformSystemProxy() systemConfig {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		return systemConfig{}
	}
	m := parseRegQuery(string(out))
	return parseWindowsProxy(m["ProxyEnable"], m["ProxyServer"], m["ProxyOverride"])
}
