package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readSystemProxy asks scutil for "System Settings → Network → Proxies".
func readSystemProxy() (sysProxy, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return sysProxy{}, err
	}
	return parseScutil(string(out)), nil
}
