//go:build !windows

package claudecli

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
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

// parentDeathGuardScript blocks on a pipe only this host holds open. Any host
// exit (SIGKILL, crash, unhandled SIGHUP) closes it, and the guard stops the
// CLI's process group. SIGTERM first so the CLI can stop the tool shells it
// started in their own groups; SIGKILL after a grace period.
const parentDeathGuardScript = `read -r _; kill -TERM -- -"$0" 2>/dev/null; sleep 5; kill -KILL -- -"$0" 2>/dev/null`

type processGuard struct {
	helper *exec.Cmd
	pipe   *os.File
	once   sync.Once
}

// guardProcessGroup stops cmd's process group (see configurePersistentCommand)
// when this host dies without closing it. macOS has no Pdeathsig, and orphaned
// CLIs otherwise keep running tools for sessions the restarted host shows idle.
func guardProcessGroup(cmd *exec.Cmd) *processGuard {
	if cmd.Process == nil {
		return nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil
	}
	defer r.Close()
	helper := exec.Command("/bin/sh", "-c", parentDeathGuardScript, strconv.Itoa(cmd.Process.Pid))
	helper.Stdin = r
	// Own group: terminal signals aimed at the host must not stop the guard early.
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := helper.Start(); err != nil {
		_ = w.Close()
		return nil
	}
	return &processGuard{helper: helper, pipe: w}
}

// release disarms the guard once the host has stopped the CLI itself. Kill the
// helper before closing the pipe so it never signals a recycled process group.
func (g *processGuard) release() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		_ = g.helper.Process.Kill()
		_ = g.helper.Wait()
		_ = g.pipe.Close()
	})
}

// configureOwnedCommand gives a context-bound CLI its own process group, so
// cancellation and the parent-death guard reach its tool children too.
func configureOwnedCommand(cmd *exec.Cmd) {
	configurePersistentCommand(cmd)
	cmd.Cancel = func() error {
		killPersistentCommand(cmd)
		return nil
	}
}
