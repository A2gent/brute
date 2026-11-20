package antigravitycli

import "os/exec"

func configureCommand(cmd *exec.Cmd) {}

func killCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
