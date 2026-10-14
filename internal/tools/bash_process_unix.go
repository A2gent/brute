//go:build !windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureBashProcess(cmd *exec.Cmd) {
	// Give each tool call its own group so cancellation cannot kill Brute or
	// another session, while including ordinary shell children in cleanup.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killBashProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
