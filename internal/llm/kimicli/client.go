package kimicli

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	defaultMaxOutputBytes = 4 * 1024 * 1024
	kimiCodePromptPrefix  = "You are running through Kimi Code CLI. Use Kimi's native tools directly when you need to inspect or modify files, then return the final answer to A2gent. Do not print JSON tool calls for A2gent to execute."
)

// Options controls how the Kimi Code CLI is invoked.
type Options struct {
	Executable string
	WorkDir    string
	Yolo       bool
}

// Client implements llm.Client by shelling out to Kimi Code CLI print mode.
type Client struct {
	model   string
	options Options
}

func NewClient(model, workDir string) *Client {
	return NewClientWithOptions(model, Options{
		WorkDir: workDir,
		Yolo:    envBoolDefault("AAGENT_KIMI_CLI_YOLO", true),
	})
}

func NewClientWithOptions(model string, options Options) *Client {
	options.Executable = normalizeExecutable(options.Executable)
	options.WorkDir = normalizeWorkDir(options.WorkDir)
	if model = normalizeModel(model); model == "" {
		model = defaultModelFromConfig()
	}
	return &Client{
		model:   model,
		options: options,
	}
}

func IsAvailable() bool {
	_, err := findExecutable("")
	return err == nil
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	response, err := c.ChatStream(ctx, request, nil)
	return response, err
}

func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	if request == nil {
		request = &llm.ChatRequest{}
	}

	model := normalizeModel(request.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		model = defaultModelFromConfig()
	}
	if model == "" {
		return nil, fmt.Errorf("Kimi CLI model is not configured")
	}

	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	prompt := buildPrompt(request)
	if systemPrompt := buildSystemPrompt(request.SystemPrompt); systemPrompt != "" {
		prompt = systemPrompt + "\n\n" + prompt
	}
	args := c.buildArgs(request, model, prompt)

	kimiPath, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Kimi CLI executable %q was not found in PATH; install Kimi Code CLI or set AAGENT_KIMI_CLI_PATH", c.options.Executable)
	}

	cmd := exec.CommandContext(ctx, kimiPath, args...)
	cmd.Dir = c.options.WorkDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open Kimi CLI stdout: %w", err)
	}
	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = defaultMaxOutputBytes
	stderr.limit = defaultMaxOutputBytes
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Kimi CLI: %w", err)
	}

	var finalContent strings.Builder
	var responseID string

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 64*1024), defaultMaxOutputBytes)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = stdout.Write([]byte(line))
		_, _ = stdout.Write([]byte("\n"))
		if strings.TrimSpace(line) == "" {
			continue
		}

		msg, err := parseStreamLine(line)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, err
		}

		switch strings.TrimSpace(msg.Role) {
		case "meta":
			if msg.SessionID != "" {
				responseID = msg.SessionID
			}
		case "assistant":
			text := messageText(msg.Content)
			if text == "" {
				continue
			}
			finalContent.WriteString(text)
			if onEvent != nil {
				if err := onEvent(llm.StreamEvent{Type: llm.StreamEventContentDelta, ContentDelta: text}); err != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					return nil, err
				}
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("failed to read Kimi CLI stream: %w", scanErr)
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("Kimi CLI failed: %s", normalizeKimiCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String())))
	}

	content := strings.TrimSpace(finalContent.String())
	if content == "" {
		return nil, fmt.Errorf("%s", normalizeKimiCLIErrorMessage(firstNonEmpty(stderr.String(), "Kimi CLI returned empty output")))
	}

	logging.LogResponseWithContent(0, 0, 0, content, nil)
	return &llm.ChatResponse{
		Content:    content,
		ResponseID: responseID,
	}, nil
}

func (c *Client) buildArgs(request *llm.ChatRequest, model, prompt string) []string {
	// WHY: print mode runs non-interactively and auto-approves Kimi's native tools,
	// which matches how A2gent delegates file/bash work to the CLI provider.
	args := []string{
		"--print",
		"-p", prompt,
		"--output-format", "stream-json",
		"-m", model,
	}
	if sessionID := strings.TrimSpace(request.SessionID); isKimiSessionID(sessionID) {
		args = append(args, "-S", sessionID)
	}
	if c.options.Yolo {
		args = append(args, "--yolo")
	}
	return args
}

var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
