//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBashTimeoutKillsChildren(t *testing.T) {
	testBashChildCleanup(t, "wait", 150, false, "timed out")
}

func TestBashCancellationKillsChildren(t *testing.T) {
	testBashChildCleanup(t, "wait", 10000, true, "canceled")
}

func TestBashExitedShellWithOpenChildPipes(t *testing.T) {
	testBashChildCleanup(t, "exit 0", 10000, false, "output pipes open")
}

func testBashChildCleanup(t *testing.T, ending string, timeout int, cancelRun bool, wantError string) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	t.Cleanup(func() {
		data, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	raw, _ := json.Marshal(BashParams{
		Command: "sleep 4 & echo $! > child.pid; printf 'started\\n'; printf 'diagnostic\\n' >&2; " + ending,
		Timeout: timeout,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cancelRun {
		timer := time.AfterFunc(150*time.Millisecond, cancel)
		defer timer.Stop()
	}
	start := time.Now()
	result, err := NewBashTool(dir).Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout waited for the child process: %v", elapsed)
	}
	if result.Success || !strings.Contains(result.Error, wantError) || !strings.Contains(result.Output, "started") || !strings.Contains(result.Output, "diagnostic") {
		t.Fatalf("expected %q with preserved output: %+v", wantError, result)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for bashChildRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if bashChildRunning(pid) {
		t.Fatalf("child %d survived timeout", pid)
	}
}

func bashChildRunning(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// Linux container PID 1 may delay reaping an orphan. A zombie is already
	// terminated and cannot hold the pipes or keep serving requests.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if end := strings.LastIndex(string(data), ") "); end >= 0 && strings.HasPrefix(string(data)[end+2:], "Z ") {
			return false
		}
	}
	return true
}

func TestBashRedirectedBackgroundCommand(t *testing.T) {
	raw := json.RawMessage(`{"command":"sleep 0.2 </dev/null >server.log 2>&1 & printf 'started\\n'","timeout":2000}`)
	result, err := NewBashTool(t.TempDir()).Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || !strings.Contains(result.Output, "started") {
		t.Fatalf("redirected background commands should succeed: %+v", result)
	}
}
