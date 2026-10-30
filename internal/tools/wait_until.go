package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultWaitUntilInterval = 5 * time.Second
	defaultWaitUntilTimeout  = 300 * time.Second
	maxWaitUntilTimeout      = 1800 * time.Second
	maxWaitUntilCheckTimeout = 60 * time.Second
	maxWaitUntilOutputTail   = 2048
)

// WaitUntilTool polls a shell condition in-process so waiting costs one LLM step
// instead of one step (and one full context re-read) per check.
type WaitUntilTool struct {
	workDir string
}

type WaitUntilParams struct {
	Command         string `json:"command"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
}

func NewWaitUntilTool(workDir string) *WaitUntilTool {
	return &WaitUntilTool{workDir: workDir}
}

func (t *WaitUntilTool) Name() string {
	return "wait_until"
}

func (t *WaitUntilTool) Description() string {
	return `Wait until a shell check succeeds (exit code 0), polling it without extra LLM steps.
Use instead of repeated polling/sleep loops (new commit, file appears, server up, process done); for delegated agents use wait_for_agent.
Returns met/timeout, attempts, elapsed time and the tail of the last check output.`
}

func (t *WaitUntilTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Shell check; the condition is met when it exits with code 0, e.g. `test -f out.log` or `curl -sf localhost:3000/health`",
			},
			"interval_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Seconds between checks (default 5, min 1)",
			},
			"timeout_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Total wait limit in seconds (default 300, max 1800)",
			},
			"cwd": map[string]interface{}{
				"type":        "string",
				"description": "Working directory for the check (optional)",
			},
		},
		"required": []string{"command"},
	}
}

func (t *WaitUntilTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p WaitUntilParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if strings.TrimSpace(p.Command) == "" {
		return &Result{Success: false, Error: "command is required"}, nil
	}
	interval := defaultWaitUntilInterval
	if p.IntervalSeconds > 0 {
		interval = time.Duration(p.IntervalSeconds) * time.Second
	}
	timeout := defaultWaitUntilTimeout
	if p.TimeoutSeconds > 0 {
		timeout = min(time.Duration(p.TimeoutSeconds)*time.Second, maxWaitUntilTimeout)
	}
	workDir := t.workDir
	if p.Cwd != "" {
		workDir = p.Cwd
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	attempts := 0
	var output string
	for {
		attempts++
		var exitCode int
		output, exitCode = runWaitCheck(waitCtx, workDir, p.Command)
		metadata := map[string]interface{}{"attempts": attempts, "elapsed_ms": time.Since(start).Milliseconds(), "exit_code": exitCode}
		if exitCode == 0 {
			return &Result{Success: true, Output: waitUntilSummary("condition met", attempts, start, output), Metadata: metadata}, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return &Result{Success: false, Error: "wait canceled", Output: waitUntilSummary("canceled", attempts, start, output), Metadata: metadata}, nil
			}
			summary := waitUntilSummary("timeout", attempts, start, output)
			return &Result{Success: false, Error: fmt.Sprintf("condition not met within %v", timeout), Output: summary, Metadata: metadata}, nil
		case <-timer.C:
		}
	}
}

func runWaitCheck(ctx context.Context, workDir, command string) (string, int) {
	checkCtx, cancel := context.WithTimeout(ctx, maxWaitUntilCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "bash", "-c", command)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	configureBashProcess(cmd)
	cmd.WaitDelay = bashOutputWaitDelay
	cmd.Cancel = func() error { return killBashProcess(cmd) }
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		_ = killBashProcess(cmd)
	}
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return out.String(), exitCode
}

func waitUntilSummary(status string, attempts int, start time.Time, output string) string {
	summary := fmt.Sprintf("%s after %d attempt(s) in %s", status, attempts, time.Since(start).Round(time.Second))
	output = strings.TrimSpace(output)
	if len(output) > maxWaitUntilOutputTail {
		output = "..." + output[len(output)-maxWaitUntilOutputTail:]
	}
	if output != "" {
		summary += "\nLast output:\n" + output
	}
	return summary
}

var _ Tool = (*WaitUntilTool)(nil)
