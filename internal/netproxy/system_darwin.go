//go:build darwin

package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readSystemProxy reads "System Settings → Network → Proxies" through scutil.
// Any failure means there is no system proxy.
func readSystemProxy() systemSettings {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return systemSettings{}
	}
	return parseDarwinProxy(string(out))
}
