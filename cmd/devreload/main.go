// Command devreload is the `just dev` hot reloader. Unlike air, which stops the
// running process before rebuilding, it builds first and restarts only once the
// server reports no active session runs or about-to-start queued sessions, so
// code edits never kill in-flight agent work.
//
// By default it runs the TUI (with its embedded API server). The TUI owns the
// terminal, so reloader and build output go to tmp/devreload.log and a one-line
// status goes to tmp/devreload.status, which the TUI shows in its bottom bar.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	binPath     = "tmp/aagent"
	nextBinPath = "tmp/aagent.next"
	logPath     = "tmp/devreload.log"
	statusPath  = "tmp/devreload.status"

	statusIdle    = "DEV: reloads on code change when idle"
	statusBuild   = "DEV: rebuilding..."
	statusFailed  = "DEV: build failed, see " + logPath
	statusPending = "DEV: new build ready, reload waits for sessions"
)

var skipDirs = map[string]bool{"tmp": true, "vendor": true, "web-app": true, "mind": true, ".git": true, ".gocache": true}

// childOwnsTerminal mutes terminal output while the TUI draws on the screen.
var childOwnsTerminal atomic.Bool

type terminalWriter struct{}

func (terminalWriter) Write(p []byte) (int, error) {
	if childOwnsTerminal.Load() {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

func main() {
	port := flag.Int("port", 5445, "API server port")
	serverMode := flag.Bool("server", false, "run headless API server instead of the TUI")
	poll := flag.Duration("poll", time.Second, "file change poll interval")
	idlePoll := flag.Duration("idle-poll", 3*time.Second, "server idle check interval while a reload is pending")
	flag.Parse()

	if err := os.MkdirAll("tmp", 0o755); err != nil {
		log.Fatal(err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()
	out := io.MultiWriter(logFile, terminalWriter{})
	log.SetOutput(out)
	log.SetPrefix("[devreload] ")

	absStatus, _ := filepath.Abs(statusPath)
	setStatus := func(s string) { _ = os.WriteFile(statusPath, []byte(s+"\n"), 0o644) }
	defer os.Remove(statusPath)

	readinessURL := fmt.Sprintf("http://127.0.0.1:%d/system/reload-readiness", *port)
	childArgs := []string{"--port", fmt.Sprint(*port)}
	if *serverMode {
		childArgs = append([]string{"server"}, childArgs...)
	}
	start := func() (*exec.Cmd, chan struct{}) {
		setStatus(statusIdle)
		return startProcess(childArgs, absStatus, !*serverMode, out)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var proc *exec.Cmd
	var procDone chan struct{}
	shutdown := func() {
		if proc != nil {
			stopProcess(proc, procDone)
		}
		os.Remove(nextBinPath)
	}
	// The TUI exiting on its own means the user quit: end `just dev` too
	// instead of relaunching. A headless server keeps waiting for a fix.
	userQuit := func() bool {
		return !*serverMode && proc != nil && processExited(procDone)
	}

	log.Printf("logs: %s", logPath)
	snapshot := sourceSnapshot(".")
	if err := build(binPath, out); err != nil {
		log.Printf("initial build failed, waiting for changes: %v", err)
	} else {
		proc, procDone = start()
	}

	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		select {
		case <-sigCh:
			shutdown()
			return
		case <-procDone:
			if userQuit() {
				shutdown()
				return
			}
			proc, procDone = nil, nil // headless server crashed: wait for the next change
		case <-ticker.C:
		}
		current := sourceSnapshot(".")
		if current == snapshot {
			continue
		}
		snapshot = waitStable(current, *poll)
		log.Printf("source changed, building")
		setStatus(statusBuild)
		if err := build(nextBinPath, out); err != nil {
			log.Printf("build failed, keeping current process: %v", err)
			setStatus(statusFailed)
			continue
		}
		if userQuit() {
			shutdown()
			return
		}

		// Defer the restart until the server is idle; a newer edit restarts the
		// cycle so we never swap in a stale binary.
		if proc != nil && !processExited(procDone) {
			setStatus(statusPending)
			changed, interrupted := waitIdle(readinessURL, *idlePoll, snapshot, sigCh)
			if interrupted || userQuit() {
				shutdown()
				return
			}
			if changed {
				continue
			}
			stopProcess(proc, procDone)
		}
		if err := os.Rename(nextBinPath, binPath); err != nil {
			log.Printf("swap binary failed: %v", err)
			continue
		}
		proc, procDone = start()
	}
}

// waitIdle blocks until the server reports idle. It returns changed=true when
// sources changed meanwhile (caller rebuilds), interrupted=true on a signal.
func waitIdle(url string, interval time.Duration, snapshot uint64, sigCh <-chan os.Signal) (changed, interrupted bool) {
	logged := false
	for {
		// Two consecutive idle reads close the short gap between a run ending
		// and a follow-up run (queued message, next queue item) registering.
		if serverIdle(url) {
			time.Sleep(300 * time.Millisecond)
			if serverIdle(url) {
				return false, false
			}
		}
		if !logged {
			log.Printf("server busy (active or about-to-start sessions), restart deferred")
			logged = true
		}
		select {
		case <-sigCh:
			return false, true
		case <-time.After(interval):
		}
		if sourceSnapshot(".") != snapshot {
			return true, false
		}
	}
}

// serverIdle treats an unreachable server or an old binary without the endpoint
// as idle: there is no work to protect and the restart should not hang.
func serverIdle(url string) bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return true
	}
	var body struct {
		Idle bool `json:"idle"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return true
	}
	return body.Idle
}

// waitStable debounces bursts of writes (formatters, multi-file edits).
func waitStable(current uint64, interval time.Duration) uint64 {
	for {
		time.Sleep(interval)
		next := sourceSnapshot(".")
		if next == current {
			return current
		}
		current = next
	}
}

// sourceSnapshot hashes path, size and mtime of non-test Go sources and module files.
func sourceSnapshot(root string) uint64 {
	h := fnv.New64a()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		watched := (strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")) || name == "go.mod" || name == "go.sum"
		if !watched {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fmt.Fprintf(h, "%s|%d|%d\n", path, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return h.Sum64()
}

func build(binary string, out io.Writer) error {
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/aagent")
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func startProcess(args []string, statusFile string, tui bool, out io.Writer) (*exec.Cmd, chan struct{}) {
	cmd := exec.Command("./"+binPath, args...)
	cmd.Env = append(os.Environ(), "AAGENT_DEV_RELOAD_STATUS="+statusFile)
	cmd.Stdin = os.Stdin
	if tui {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stdout = out
		cmd.Stderr = out
	}
	done := make(chan struct{})
	if err := cmd.Start(); err != nil {
		log.Printf("start failed: %v", err)
		close(done)
		return nil, done
	}
	childOwnsTerminal.Store(tui)
	log.Printf("started pid %d: %s %s", cmd.Process.Pid, binPath, strings.Join(args, " "))
	go func() {
		err := cmd.Wait()
		childOwnsTerminal.Store(false)
		log.Printf("process exited: %v", err)
		close(done)
	}()
	return cmd, done
}

func processExited(done chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// stopProcess sends SIGTERM: Bubble Tea treats it as a clean quit (restoring the
// terminal) and the headless server shuts down gracefully on it.
func stopProcess(cmd *exec.Cmd, done chan struct{}) {
	if cmd == nil || processExited(done) {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		log.Printf("process did not stop in time, killing")
		_ = cmd.Process.Kill()
		<-done
	}
}
