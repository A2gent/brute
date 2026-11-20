//go:build !windows

package antigravitycli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

func startFakeAgyWithChild(t *testing.T, output string) string {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	writeFakeAgy(t, "sleep 60 &\necho $! > "+pidFile+"\n"+output+"wait\n")
	return pidFile
}

func waitChildGone(t *testing.T, pidFile string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(pidFile)
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err == nil && syscall.Kill(pid, 0) != nil {
			return
		}
		if time.Now().After(deadline) {
			if err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			t.Fatalf("agy child process survived (pid file %q)", raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestChatStreamCancelKillsAgyChildren(t *testing.T) {
	pidFile := startFakeAgyWithChild(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(pidFile); err == nil {
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	client := NewClientWithOptions("", Options{WorkDir: t.TempDir()})
	start := time.Now()
	_, err := client.Chat(ctx, &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
	waitChildGone(t, pidFile)
}

func TestChatStreamOversizedLineKillsAgy(t *testing.T) {
	pidFile := startFakeAgyWithChild(t, "head -c 5000000 /dev/zero | tr '\\0' a\necho\n")
	client := NewClientWithOptions("", Options{WorkDir: t.TempDir()})
	start := time.Now()
	_, err := client.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "failed to read Antigravity CLI stream") {
		t.Fatalf("expected stream read error, got %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("oversized line handling took %v", time.Since(start))
	}
	waitChildGone(t, pidFile)
}
