//go:build !windows

package claudecli

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func startGuardedSleep(t *testing.T) (*exec.Cmd, *processGuard, <-chan error) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	configurePersistentCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { killPersistentCommand(cmd) })
	guard := guardProcessGroup(cmd)
	if guard == nil {
		t.Fatal("guardProcessGroup returned nil")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, guard, done
}

func TestGuardProcessGroup(t *testing.T) {
	t.Run("host death stops the CLI group", func(t *testing.T) {
		_, guard, done := startGuardedSleep(t)
		t.Cleanup(guard.release)

		// The OS closes the host's pipe end on exit, however the host died.
		_ = guard.pipe.Close()

		select {
		case err := <-done:
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected signal exit, got %v", err)
			}
			if ws := exitErr.Sys().(syscall.WaitStatus); ws.Signal() != syscall.SIGTERM {
				t.Fatalf("expected SIGTERM, got %v", ws.Signal())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("guarded process survived host death")
		}
	})

	t.Run("release leaves the CLI to its owner", func(t *testing.T) {
		_, guard, done := startGuardedSleep(t)

		guard.release()

		select {
		case err := <-done:
			t.Fatalf("released guard stopped the process: %v", err)
		case <-time.After(500 * time.Millisecond):
		}
	})
}
