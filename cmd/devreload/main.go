// Command devreload is the `just dev` hot reloader. Unlike air, which stops the
// running server before rebuilding, it builds first and restarts only once the
// server reports no active session runs or about-to-start queued sessions, so
// code edits never kill in-flight agent work.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	binPath     = "tmp/aagent"
	nextBinPath = "tmp/aagent.next"
)

var skipDirs = map[string]bool{"tmp": true, "vendor": true, "web-app": true, "mind": true, ".git": true, ".gocache": true}

func main() {
	port := flag.Int("port", 5445, "server port")
	poll := flag.Duration("poll", time.Second, "file change poll interval")
	idlePoll := flag.Duration("idle-poll", 3*time.Second, "server idle check interval while a reload is pending")
	flag.Parse()
	log.SetPrefix("[devreload] ")

	readinessURL := fmt.Sprintf("http://127.0.0.1:%d/system/reload-readiness", *port)
	serverArgs := []string{"server", "--port", fmt.Sprint(*port)}

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

	snapshot := sourceSnapshot(".")
	if err := build(binPath); err != nil {
		log.Printf("initial build failed, waiting for changes: %v", err)
	} else {
		proc, procDone = startProcess(serverArgs)
	}

	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		select {
		case <-sigCh:
			shutdown()
			return
		case <-ticker.C:
		}
		current := sourceSnapshot(".")
		if current == snapshot {
			continue
		}
		snapshot = waitStable(current, *poll)
		log.Printf("source changed, building")
		if err := build(nextBinPath); err != nil {
			log.Printf("build failed, keeping current server: %v", err)
			continue
		}

		// Defer the restart until the server is idle; a newer edit restarts the
		// cycle so we never swap in a stale binary.
		if proc != nil && !processExited(procDone) {
			changed, interrupted := waitIdle(readinessURL, *idlePoll, snapshot, sigCh)
			if interrupted {
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
		proc, procDone = startProcess(serverArgs)
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

func build(out string) error {
	cmd := exec.Command("go", "build", "-o", out, "./cmd/aagent")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startProcess(args []string) (*exec.Cmd, chan struct{}) {
	cmd := exec.Command("./"+binPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	done := make(chan struct{})
	if err := cmd.Start(); err != nil {
		log.Printf("start failed: %v", err)
		close(done)
		return nil, done
	}
	log.Printf("server started (pid %d)", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		log.Printf("server exited: %v", err)
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

func stopProcess(cmd *exec.Cmd, done chan struct{}) {
	if cmd == nil || processExited(done) {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		log.Printf("server did not stop in time, killing")
		_ = cmd.Process.Kill()
		<-done
	}
}
