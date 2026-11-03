package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readPlatformProxy reads the macOS proxy settings from "scutil --proxy".
// Any failure means there is no system proxy.
func readPlatformProxy() systemProxy {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return systemProxy{}
	}
	return parseScutil(string(out))
}
