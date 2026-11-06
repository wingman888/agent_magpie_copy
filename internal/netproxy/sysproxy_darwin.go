//go:build darwin

package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readSystem reads the macOS proxy from `scutil --proxy`. A failure means
// there is no system proxy.
func readSystem() sysConfig {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return sysConfig{}
	}
	return parseScutil(string(out))
}
