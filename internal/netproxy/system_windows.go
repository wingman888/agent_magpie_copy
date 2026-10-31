package netproxy

import (
	"context"
	"os/exec"
	"time"
)

func readSystem() *SystemConfig {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return ParseWinReg(string(out))
}
