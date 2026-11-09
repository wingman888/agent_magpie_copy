//go:build windows

package netproxy

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps the reg program from opening a console window when magpie
// runs as a desktop app.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
