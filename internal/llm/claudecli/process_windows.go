package claudecli

import "os/exec"

func configurePersistentCommand(cmd *exec.Cmd) {}
func killPersistentCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

type processGuard struct{}

func guardProcessGroup(cmd *exec.Cmd) *processGuard { return nil }
func (g *processGuard) release()                    {}
func configureOwnedCommand(cmd *exec.Cmd)           {}
