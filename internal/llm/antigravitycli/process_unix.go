//go:build !windows

package antigravitycli

import (
	"os/exec"
	"syscall"
)

// agy runs shell tools as children; cancellation must reach the whole group.
func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killCommand(cmd)
		return nil
	}
}

func killCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
