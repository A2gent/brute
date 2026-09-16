package speechengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var lookPath = exec.LookPath

type commandResult struct {
	stdout []byte
	stderr []byte
}

func runExternal(ctx context.Context, binary string, args ...string) (commandResult, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return commandResult{}, errors.New("executable path is empty")
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := commandResult{
		stdout: stdout.Bytes(),
		stderr: stderr.Bytes(),
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = err.Error()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result, err
		}
		return result, fmt.Errorf("%s failed: %s", filepathBase(binary), detail)
	}
	return result, nil
}

func filepathBase(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "command"
	}
	if idx := strings.LastIndexAny(path, `/\`); idx >= 0 && idx+1 < len(path) {
		return path[idx+1:]
	}
	return path
}

func ensureRegularFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return ErrInvalidAudioPath
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("audio file not accessible: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("audio path is a directory: %s", path)
	}
	return nil
}
