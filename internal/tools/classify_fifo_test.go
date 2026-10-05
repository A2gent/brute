//go:build darwin || linux

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm/jev"
)

func TestClassifyRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan *Result, 1)
	tool := NewClassifyTool(dir, jev.NewClient("key", "", "http://127.0.0.1:1"))
	go func() {
		result, _ := tool.Execute(context.Background(), json.RawMessage(`{"path":"pipe","question":"Q?","type":"noul"}`))
		done <- result
	}()
	select {
	case result := <-done:
		if result.Success || !strings.Contains(result.Error, "regular file") {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		// Unblock the old implementation so the failing test leaves no stuck goroutine.
		file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			file.Close()
		}
		<-done
		t.Fatal("opening FIFO blocked past timeout")
	}
}
