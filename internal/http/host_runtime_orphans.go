// host_runtime_orphans.go reaps host agents left behind by a previous parent
// (SIGKILL, crash, or a binary that predates the parent watchdog). Each spawn
// records agent.pid next to agent.log; the next parent kills what it finds.
package http

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/A2gent/brute/internal/logging"
)

const hostAgentPIDFileName = "agent.pid"

type hostAgentPIDRecord struct {
	PID  int    `json:"pid"`
	Port int    `json:"port"`
	Exe  string `json:"exe"`
}

func (r hostAgentPIDRecord) command() string {
	return fmt.Sprintf("%s server --port %d", r.Exe, r.Port)
}

// inspectHostProcess reports a live process's parent pid and command line.
// A variable so tests can fake an orphan without actually being reparented.
var inspectHostProcess = func(pid int) (ppid int, command string, ok bool) {
	out, err := exec.Command("ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", false
	}
	fields := strings.SplitN(strings.TrimSpace(string(out)), " ", 2)
	if len(fields) != 2 {
		return 0, "", false
	}
	ppid, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, "", false
	}
	return ppid, strings.TrimSpace(fields[1]), true
}

func writeHostAgentPIDFile(dataDir string, rec hostAgentPIDRecord) {
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dataDir, hostAgentPIDFileName), raw, 0o644); err != nil {
		logging.Warn("Host runtime: failed to record pid file in %s: %v", dataDir, err)
	}
}

// removeHostAgentPIDFile deletes the pid file only if it still names pid, so a
// late exit of an old process cannot erase its replacement's record.
func removeHostAgentPIDFile(dataDir string, pid int) {
	path := filepath.Join(dataDir, hostAgentPIDFileName)
	if rec, ok := readHostAgentPIDFile(path); ok && rec.PID == pid {
		_ = os.Remove(path)
	}
}

func readHostAgentPIDFile(path string) (hostAgentPIDRecord, bool) {
	var rec hostAgentPIDRecord
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &rec) != nil || rec.PID <= 1 {
		return rec, false
	}
	return rec, true
}

// reapHostAgentPIDFile kills the process a pid file names when it is still our
// orphan: reparented to init/launchd and running the exact command we spawned.
// Anything else (dead pid, reused pid, child of a live parent) is left alone.
func reapHostAgentPIDFile(dataDir string) bool {
	path := filepath.Join(dataDir, hostAgentPIDFileName)
	rec, ok := readHostAgentPIDFile(path)
	if !ok {
		_ = os.Remove(path)
		return false
	}
	ppid, command, alive := inspectHostProcess(rec.PID)
	if alive && ppid != 1 {
		return false // a live parent owns it
	}
	if !alive || command != rec.command() {
		_ = os.Remove(path)
		return false
	}
	logging.Info("Host runtime: killing orphaned agent pid %d (port %d) from %s", rec.PID, rec.Port, dataDir)
	_ = syscall.Kill(-rec.PID, syscall.SIGTERM)
	deadline := time.Now().Add(hostRuntimeStopTimeout)
	for syscall.Kill(rec.PID, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(rec.PID, 0) == nil {
		_ = syscall.Kill(-rec.PID, syscall.SIGKILL)
	}
	_ = os.Remove(path)
	return true
}

// reapOrphanedHostAgents sweeps every local agent data dir once at startup.
func reapOrphanedHostAgents() {
	logPath, err := hostAgentLogPath("x")
	if err != nil {
		return
	}
	root := filepath.Dir(filepath.Dir(logPath))
	matches, _ := filepath.Glob(filepath.Join(root, "*", hostAgentPIDFileName))
	reaped := 0
	for _, pidFile := range matches {
		if reapHostAgentPIDFile(filepath.Dir(pidFile)) {
			reaped++
		}
	}
	if reaped > 0 {
		logging.Info("Host runtime: reaped %d orphaned host agent(s)", reaped)
	}
}
