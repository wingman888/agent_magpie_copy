//go:build !windows

package netproxy

import "os/exec"

func hideWindow(*exec.Cmd) {}
