package netproxy

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const internetSettings = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func loadSystem() systemProxy {
	return parseWindows(regValue("ProxyEnable"), regValue("ProxyServer"), regValue("ProxyOverride"))
}

// regValue returns a value of the Internet Options key, or "" when it is
// missing or reg.exe fails.
func regValue(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "reg", "query", internetSettings, "/v", name).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && strings.EqualFold(f[0], name) {
			return strings.Join(f[2:], " ")
		}
	}
	return ""
}
