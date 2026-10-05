package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultBashTimeout = 30 * time.Second
	maxOutputSize      = 50 * 1024 // code_execution legacy output limit
)

// BashTool executes shell commands
type BashTool struct {
	workDir string
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

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Description() string {
	return `Execute shell commands in the project environment.
Use this for running terminal commands like git, npm, make, etc.
Commands run in the project's working directory by default.`
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
				"description": "Timeout in milliseconds (default: 120000)",
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

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

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
		if ctx.Err() == context.DeadlineExceeded {
			return &Result{
				Success:  false,
				Error:    fmt.Sprintf("command timed out after %v", timeout),
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
