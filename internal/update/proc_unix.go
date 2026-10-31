//go:build !windows

package update

import (
	"os"
	"os/exec"
	"syscall"
)

// alive reports whether pid is still running. A relaunched magpie is that
// process's child, so its exit shows as a new parent even before the
// process table lets go of it.
func alive(pid int) bool {
	if os.Getppid() != pid {
		return syscall.Kill(pid, 0) == nil
	}
	return true
}

// detach keeps a relaunched magpie out of this one's process group, so it
// outlives it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
