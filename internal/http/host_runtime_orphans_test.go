package http

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// startFakeHostAgent runs the test binary as a host agent stand-in in its own
// process group, exactly as spawn does, and returns its pid record.
func startFakeHostAgent(t *testing.T) (*exec.Cmd, hostAgentPIDRecord) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "server", "--port", "0") // 0: any free port
	cmd.Env = append(os.Environ(), hostAgentHelperEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	})
	return cmd, hostAgentPIDRecord{PID: cmd.Process.Pid, Port: 0, Exe: os.Args[0]}
}

func fakeInspect(t *testing.T, ppid int, command string) {
	t.Helper()
	orig := inspectHostProcess
	inspectHostProcess = func(pid int) (int, string, bool) {
		if syscall.Kill(pid, 0) != nil {
			return 0, "", false
		}
		return ppid, command, true
	}
	t.Cleanup(func() { inspectHostProcess = orig })
}

func waitProcessGone(pid int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// Reaped zombies vanish; the Wait goroutine in startFakeHostAgent reaps it.
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestReapHostAgentPIDFileKillsOrphan(t *testing.T) {
	dataDir := t.TempDir()
	_, rec := startFakeHostAgent(t)
	writeHostAgentPIDFile(dataDir, rec)
	fakeInspect(t, 1, rec.command())

	if !reapHostAgentPIDFile(dataDir) {
		t.Fatal("expected the orphan to be reaped")
	}
	if !waitProcessGone(rec.PID) {
		t.Fatal("orphan process still alive")
	}
	if _, err := os.Stat(filepath.Join(dataDir, hostAgentPIDFileName)); !os.IsNotExist(err) {
		t.Fatalf("pid file must be removed, stat err=%v", err)
	}
}

func TestReapHostAgentPIDFileSparesNonOrphans(t *testing.T) {
	t.Run("child of a live parent", func(t *testing.T) {
		dataDir := t.TempDir()
		_, rec := startFakeHostAgent(t)
		writeHostAgentPIDFile(dataDir, rec)
		fakeInspect(t, os.Getpid(), rec.command())

		if reapHostAgentPIDFile(dataDir) {
			t.Fatal("must not reap a process whose parent is alive")
		}
		if syscall.Kill(rec.PID, 0) != nil {
			t.Fatal("process must survive")
		}
		if _, err := os.Stat(filepath.Join(dataDir, hostAgentPIDFileName)); err != nil {
			t.Fatal("pid file of a live parent's child must be kept")
		}
	})

	t.Run("pid reused by another program", func(t *testing.T) {
		dataDir := t.TempDir()
		_, rec := startFakeHostAgent(t)
		writeHostAgentPIDFile(dataDir, rec)
		fakeInspect(t, 1, "/usr/bin/something-else")

		if reapHostAgentPIDFile(dataDir) {
			t.Fatal("must not kill an unrelated process")
		}
		if syscall.Kill(rec.PID, 0) != nil {
			t.Fatal("unrelated process must survive")
		}
		if _, err := os.Stat(filepath.Join(dataDir, hostAgentPIDFileName)); !os.IsNotExist(err) {
			t.Fatal("stale pid file must be removed")
		}
	})

	t.Run("dead pid", func(t *testing.T) {
		dataDir := t.TempDir()
		cmd := exec.Command("true")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		writeHostAgentPIDFile(dataDir, hostAgentPIDRecord{PID: cmd.Process.Pid, Port: 1, Exe: "true"})
		if reapHostAgentPIDFile(dataDir) {
			t.Fatal("nothing to reap for a dead pid")
		}
		if _, err := os.Stat(filepath.Join(dataDir, hostAgentPIDFileName)); !os.IsNotExist(err) {
			t.Fatal("stale pid file must be removed")
		}
	})
}

func TestRemoveHostAgentPIDFileKeepsReplacementRecord(t *testing.T) {
	dataDir := t.TempDir()
	writeHostAgentPIDFile(dataDir, hostAgentPIDRecord{PID: 222, Port: 1, Exe: "x"})
	removeHostAgentPIDFile(dataDir, 111) // the old process exits late
	if rec, ok := readHostAgentPIDFile(filepath.Join(dataDir, hostAgentPIDFileName)); !ok || rec.PID != 222 {
		t.Fatalf("replacement record must survive, got %+v %v", rec, ok)
	}
	removeHostAgentPIDFile(dataDir, 222)
	if _, err := os.Stat(filepath.Join(dataDir, hostAgentPIDFileName)); !os.IsNotExist(err) {
		t.Fatal("own record must be removed")
	}
}

func TestInspectHostProcessReadsRealCommand(t *testing.T) {
	cmd, rec := startFakeHostAgent(t)
	ppid, command, ok := inspectHostProcess(cmd.Process.Pid)
	if !ok || ppid != os.Getpid() || command != rec.command() {
		t.Fatalf("got ppid=%d command=%q ok=%v, want ppid=%d command=%q", ppid, command, ok, os.Getpid(), rec.command())
	}
}
