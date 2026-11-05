package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readSystem reads the macOS network proxy settings. Any failure means there
// is no system proxy.
func readSystem() sysConf {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return sysConf{}
	}
	return parseScutil(string(out))
}
