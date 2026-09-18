package speechengine

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBoundedStreamWriterConcurrentWrites(t *testing.T) {
	var mu sync.Mutex
	lines := make([]string, 0, 200)
	appendLine := func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	}

	w := newBoundedStreamWriter(appendLine, 1024)
	const goroutines = 8
	const perGoroutine = 10000
	errCh := make(chan error, goroutines*2)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			payload := strings.Repeat("x", 80) + "\n"
			for i := 0; i < perGoroutine; i++ {
				if _, err := w.Write([]byte(payload)); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
		go func(id int) {
			defer wg.Done()
			payload := strings.Repeat("y", 80) + "\n"
			for i := 0; i < perGoroutine; i++ {
				if _, err := w.Write([]byte(payload)); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(lines) != goroutines*2*perGoroutine {
		t.Fatalf("expected %d lines, got %d", goroutines*2*perGoroutine, len(lines))
	}
}

func TestRunStreamingHighVolumeSubprocess(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("python not available for subprocess streaming test")
	}

	const lines = 70000
	script := `
import sys
for i in range(` + "70000" + `):
    print(f"out-{i}", flush=True)
    print(f"err-{i}", file=sys.stderr, flush=True)
`
	var mu sync.Mutex
	var outLines, errLines int
	appendLine := func(line string) {
		mu.Lock()
		if strings.HasPrefix(line, "out-") {
			outLines++
		}
		if strings.HasPrefix(line, "err-") {
			errLines++
		}
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runner := execStreamingRunner{}
	if err := runner.RunStreaming(ctx, python, []string{"-c", script}, appendLine, nil); err != nil {
		t.Fatalf("RunStreaming failed: %v", err)
	}
	if outLines != lines {
		t.Fatalf("expected %d stdout lines, got %d", lines, outLines)
	}
	if errLines != lines {
		t.Fatalf("expected %d stderr lines, got %d", lines, errLines)
	}
}

func TestStreamWriterBoundsUnterminatedProgressAndFlushes(t *testing.T) {
	var output strings.Builder
	w := newBoundedStreamWriter(func(line string) { output.WriteString(line) }, 1024)
	input := strings.Repeat("x", 200000) + "tail"
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if len(w.lineBuf) >= 1024 {
		t.Fatalf("pending log unbounded: %d", len(w.lineBuf))
	}
	w.Flush()
	if output.String() != input {
		t.Fatal("unterminated log output lost")
	}
}
