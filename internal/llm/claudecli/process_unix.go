//go:build !windows

package claudecli

import (
	"os/exec"
	"syscall"
)

func configurePersistentCommand(cmd *exec.Cmd) {
	// Native tools and any stdio MCP children must not survive cancellation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killPersistentCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
