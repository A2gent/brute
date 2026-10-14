package claudecli

import "os/exec"

func configurePersistentCommand(cmd *exec.Cmd) {}
func killPersistentCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
