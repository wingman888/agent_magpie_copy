package netproxy

import (
	"context"
	"os/exec"
	"time"
)

func readSystem() *SystemConfig {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return nil
	}
	return ParseScutil(string(out))
}
