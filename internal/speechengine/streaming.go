package speechengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type streamAppender func(line string)

type streamingRunner interface {
	RunStreaming(ctx context.Context, name string, args []string, appendLine streamAppender, env []string) error
}

func runCommand(ctx context.Context, p *platformEnv, name string, args []string, appendLine streamAppender, env []string) (stdout, stderr []byte, err error) {
	if sr, ok := p.runner.(streamingRunner); ok && appendLine != nil {
		err = sr.RunStreaming(ctx, name, args, appendLine, env)
		return nil, nil, err
	}
	return p.runner.Run(ctx, name, args...)
}

// boundedStreamWriter is a concurrency-safe io.Writer for cmd.Stdout and cmd.Stderr.
type boundedStreamWriter struct {
	mu         sync.Mutex
	appendLine streamAppender
	lineBuf    []byte
	retained   []byte
	maxBytes   int
}

func newBoundedStreamWriter(appendLine streamAppender, maxBytes int) *boundedStreamWriter {
	return &boundedStreamWriter{
		appendLine: appendLine,
		maxBytes:   maxBytes,
	}
}

func (w *boundedStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.retain(p)
	w.lineBuf = append(w.lineBuf, p...)
	for {
		idx := indexLineBreak(w.lineBuf)
		if idx < 0 {
			// Progress output may have no line breaks; bound the pending fragment too.
			if w.maxBytes > 0 && len(w.lineBuf) >= w.maxBytes {
				if w.appendLine != nil {
					w.appendLine(string(w.lineBuf[:w.maxBytes]))
				}
				w.lineBuf = w.lineBuf[w.maxBytes:]
				continue
			}
			break
		}
		line := string(w.lineBuf[:idx])
		w.lineBuf = w.lineBuf[idx:]
		if len(w.lineBuf) > 0 && (w.lineBuf[0] == '\n' || w.lineBuf[0] == '\r') {
			w.lineBuf = w.lineBuf[1:]
		}
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if w.appendLine != nil {
			w.appendLine(line)
		}
	}
	return len(p), nil
}

func (w *boundedStreamWriter) retain(p []byte) {
	if w.maxBytes <= 0 {
		return
	}
	w.retained = append(w.retained, p...)
	if len(w.retained) <= w.maxBytes {
		return
	}
	w.retained = w.retained[len(w.retained)-w.maxBytes:]
}

func indexLineBreak(buf []byte) int {
	for i, b := range buf {
		if b == '\n' || b == '\r' {
			return i
		}
	}
	return -1
}

func (w *boundedStreamWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lineBuf) > 0 && w.appendLine != nil {
		w.appendLine(string(w.lineBuf))
	}
	w.lineBuf = nil
}

type execStreamingRunner struct{}

func (execStreamingRunner) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	result, runErr := runExternal(ctx, name, args...)
	return result.stdout, result.stderr, runErr
}

func (execStreamingRunner) RunStreaming(ctx context.Context, name string, args []string, appendLine streamAppender, env []string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("executable path is empty")
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	if len(env) > 0 {
		cmd.Env = env
	}

	writer := newBoundedStreamWriter(appendLine, maxInstallLogBytes)
	defer writer.Flush()
	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%s failed: %w", filepathBase(name), err)
	}
	return nil
}

// compile-time check that boundedStreamWriter satisfies io.Writer
var _ io.Writer = (*boundedStreamWriter)(nil)
