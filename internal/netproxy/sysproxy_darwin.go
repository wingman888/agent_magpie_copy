//go:build darwin

package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readOSProxy reads "System Settings → Network → Proxies" through scutil.
// Any failure means no system proxy.
func readOSProxy() sysProxy {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return sysProxy{}
	}
	return parseScutil(string(out))
}
