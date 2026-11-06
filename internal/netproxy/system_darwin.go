package netproxy

import (
	"context"
	"os/exec"
	"time"
)

func loadSystem() systemProxy {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return systemProxy{}
	}
	return parseScutil(string(out))
}
