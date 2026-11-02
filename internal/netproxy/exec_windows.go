package netproxy

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a GUI process from flashing a console for reg.exe.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
