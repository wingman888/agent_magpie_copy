//go:build darwin

package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readSystem reads "System Settings → Network → Proxies" through
// `scutil --proxy`. A failure means there is no system proxy.
func readSystem() sysProxy {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return sysProxy{}
	}
	p, except := parseScutil(string(out))
	return sysProxy{URL: p, Except: except}
}
