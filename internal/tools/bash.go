package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultBashTimeout  = 30 * time.Second
	bashOutputWaitDelay = time.Second
	maxOutputSize       = 50 * 1024 // code_execution legacy output limit
)

// BashTool executes shell commands
type BashTool struct {
	workDir string
	env     []string // extra KEY=VALUE pairs on top of the server environment
}

// BashParams defines parameters for the bash tool
type BashParams struct {
	Command string `json:"command"`
	WorkDir string `json:"workdir,omitempty"`
	Timeout int    `json:"timeout,omitempty"` // milliseconds
}

// NewBashTool creates a new bash tool
func NewBashTool(workDir string) *BashTool {
	return &BashTool{workDir: workDir}
}

// NewBashToolWithEnv creates a bash tool whose commands also see env, e.g. the
// worktree slot variables of a session working in a git worktree.
func NewBashToolWithEnv(workDir string, env []string) *BashTool {
	return &BashTool{workDir: workDir, env: env}
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Description() string {
	return `Execute shell commands in the project environment.
Use this for running terminal commands like git, npm, make, etc.
Commands run in the project's working directory by default.
Timeouts are in milliseconds and terminate the command and its child processes.
For long-lived preview/dev servers, launch in the background with stdin from /dev/null
and stdout/stderr redirected to a log file, and record the PID for cleanup.`
}

func (t *BashTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "Working directory for the command (optional)",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in milliseconds (default: 30000). On timeout, the command and its child processes are terminated.",
			},
		},
		"required": []string{"command"},
	}
}

func (t *BashTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p BashParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if p.Command == "" {
		return &Result{Success: false, Error: "command is required"}, nil
	}

	// Determine working directory
	workDir := t.workDir
	if p.WorkDir != "" {
		workDir = p.WorkDir
	}

	// Determine timeout
	timeout := defaultBashTimeout
	if p.Timeout > 0 {
		timeout = time.Duration(p.Timeout) * time.Millisecond
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Execute command
	cmd := exec.CommandContext(ctx, "bash", "-c", p.Command)
	cmd.Dir = workDir
	if len(t.env) > 0 {
		cmd.Env = append(os.Environ(), t.env...)
	}
	configureBashProcess(cmd)
	// A child can inherit the output pipes even after Bash exits, or escape its
	// process group. Bound pipe draining so neither case can stall the agent.
	cmd.WaitDelay = bashOutputWaitDelay
	cmd.Cancel = func() error { return killBashProcess(cmd) }

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// Bash may have exited before cancellation, leaving children holding its
		// pipes open. Clean up the group when the bounded pipe wait returns too.
		_ = killBashProcess(cmd)
	}

	// Combine output
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	// Keep the original lossless for session-scoped context_retrieve. Large logs
	// are summarized deterministically by the request compressor.
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	output += fmt.Sprintf("\nExit code: %d", exitCode)
	metadata := map[string]interface{}{"exit_code": exitCode}
	if stderr.Len() > 0 {
		start := 1
		if stdout.Len() > 0 {
			start += strings.Count(stdout.String(), "\n") + 1
		}
		metadata["stderr_start_line"] = start
	}

	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return &Result{Success: false, Error: "command canceled", Output: output, Metadata: metadata}, nil
		}
		if ctx.Err() == context.DeadlineExceeded {
			return &Result{
				Success:  false,
				Error:    fmt.Sprintf("command timed out after %v", timeout),
				Output:   output,
				Metadata: metadata,
			}, nil
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			return &Result{
				Success:  false,
				Error:    "command exited but child processes kept output pipes open; launch background servers with stdin from /dev/null and stdout/stderr redirected to a log file",
				Output:   output,
				Metadata: metadata,
			}, nil
		}

		// Command failed but we still want to return output
		return &Result{
			Success:  false,
			Error:    fmt.Sprintf("command failed: %v", err),
			Output:   output,
			Metadata: metadata,
		}, nil
	}

	return &Result{
		Success:  true,
		Output:   output,
		Metadata: metadata,
	}, nil
}

// Ensure BashTool implements Tool
var _ Tool = (*BashTool)(nil)
