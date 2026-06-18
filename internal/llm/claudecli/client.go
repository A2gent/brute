package claudecli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	defaultExecutable      = "claude"
	defaultMaxOutputBytes  = 4 * 1024 * 1024
	defaultPermissionMode  = "acceptEdits"
	claudeCodePromptPrefix = "You are running through Claude Code CLI. Use Claude Code's native tools directly when you need to inspect or modify files, then return the final answer to A2gent. Do not print JSON tool calls for A2gent to execute."
)

// Options controls how the Claude Code CLI is invoked.
type Options struct {
	Executable           string
	WorkDir              string
	PermissionMode       string
	MaxBudgetUSD         string
	NoSessionPersistence bool
}

// Client implements llm.Client by shelling out to Claude Code CLI.
type Client struct {
	model   string
	options Options
}

func NewClient(model, workDir string) *Client {
	return NewClientWithOptions(model, Options{
		WorkDir:              workDir,
		NoSessionPersistence: envBoolDefault("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", true),
		PermissionMode:       strings.TrimSpace(os.Getenv("AAGENT_CLAUDE_CLI_PERMISSION_MODE")),
		MaxBudgetUSD:         strings.TrimSpace(os.Getenv("AAGENT_CLAUDE_CLI_MAX_BUDGET_USD")),
	})
}

func NewClientWithOptions(model string, options Options) *Client {
	options.Executable = normalizeExecutable(options.Executable)
	options.WorkDir = normalizeWorkDir(options.WorkDir)
	options.PermissionMode = strings.TrimSpace(options.PermissionMode)
	options.MaxBudgetUSD = strings.TrimSpace(options.MaxBudgetUSD)
	return &Client{
		model:   normalizeModel(model),
		options: options,
	}
}

func IsAvailable() bool {
	_, err := findExecutable(normalizeExecutable(""))
	return err == nil
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	if request == nil {
		request = &llm.ChatRequest{}
	}

	model := normalizeModel(request.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return nil, fmt.Errorf("Claude CLI model is not configured")
	}

	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	prompt := buildPrompt(request)
	args := c.buildArgs(request, model, prompt)

	claudePath, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Claude CLI executable %q was not found in PATH; install Claude Code or set AAGENT_CLAUDE_CLI_PATH", c.options.Executable)
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = c.options.WorkDir

	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = defaultMaxOutputBytes
	stderr.limit = defaultMaxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("Claude CLI failed: %s", normalizeClaudeCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String())))
	}

	parsed, raw, err := parseCLIResult(stdout.String())
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(parsed.Result)
	if content == "" {
		content = strings.TrimSpace(parsed.Message)
	}
	if content == "" {
		content = strings.TrimSpace(raw)
	}
	if parsed.IsError || strings.Contains(strings.ToLower(parsed.Subtype), "error") {
		msg := content
		if msg == "" {
			msg = strings.TrimSpace(parsed.Error)
		}
		if msg == "" {
			msg = "Claude CLI returned an error result"
		}
		return nil, fmt.Errorf("%s", normalizeClaudeCLIErrorMessage(msg))
	}

	usage := usageFromRaw(parsed.Usage)
	logging.LogResponseWithContent(usage.InputTokens, usage.OutputTokens, 0, content, nil)
	return &llm.ChatResponse{
		Content:    content,
		Usage:      usage,
		StopReason: firstNonEmpty(parsed.StopReason, parsed.Subtype),
		ResponseID: strings.TrimSpace(parsed.SessionID),
	}, nil
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
		return nil, fmt.Errorf("Claude CLI model is not configured")
	}

	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	prompt := buildPrompt(request)
	args := c.buildStreamArgs(request, model, prompt)

	claudePath, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Claude CLI executable %q was not found in PATH; install Claude Code or set AAGENT_CLAUDE_CLI_PATH", c.options.Executable)
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = c.options.WorkDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open Claude CLI stdout: %w", err)
	}
	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = defaultMaxOutputBytes
	stderr.limit = defaultMaxOutputBytes
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Claude CLI: %w", err)
	}

	var content strings.Builder
	var assistantContent string
	var usage llm.TokenUsage
	var responseID string
	var stopReason string
	var finalResult cliResult
	var sawResult bool

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 64*1024), defaultMaxOutputBytes)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = stdout.Write([]byte(line))
		_, _ = stdout.Write([]byte("\n"))
		if strings.TrimSpace(line) == "" {
			continue
		}
		event, err := parseCLIStreamEnvelope(line)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, err
		}
		if event.SessionID != "" {
			responseID = event.SessionID
		}
		switch event.Type {
		case "stream_event":
			if event.Event.Message.ID != "" && responseID == "" {
				responseID = event.Event.Message.ID
			}
			if event.Event.Message.Usage != nil {
				usage = mergeUsage(usage, usageFromRaw(event.Event.Message.Usage))
			}
			if event.Event.Usage != nil {
				usage = mergeUsage(usage, usageFromRaw(event.Event.Usage))
			}
			if event.Event.Delta.StopReason != "" {
				stopReason = event.Event.Delta.StopReason
			}
			if event.Event.Type == "content_block_delta" &&
				event.Event.Delta.Type == "text_delta" &&
				event.Event.Delta.Text != "" {
				content.WriteString(event.Event.Delta.Text)
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{Type: llm.StreamEventContentDelta, ContentDelta: event.Event.Delta.Text}); err != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						return nil, err
					}
				}
			}
		case "assistant":
			assistantContent = streamMessageText(event.Message)
			if event.Message.ID != "" && responseID == "" {
				responseID = event.Message.ID
			}
			if event.Message.Usage != nil {
				usage = mergeUsage(usage, usageFromRaw(event.Message.Usage))
			}
			if event.Message.StopReason != "" {
				stopReason = event.Message.StopReason
			}
		case "result":
			sawResult = true
			finalResult = cliResult{
				Type:       event.Type,
				Subtype:    event.Subtype,
				IsError:    event.IsError,
				Result:     event.Result,
				Error:      event.Error,
				SessionID:  event.SessionID,
				StopReason: event.StopReason,
				Usage:      event.Usage,
			}
			if event.Usage != nil {
				usage = mergeUsage(usage, usageFromRaw(event.Usage))
			}
			if event.StopReason != "" {
				stopReason = event.StopReason
			}
			if event.SessionID != "" {
				responseID = event.SessionID
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("failed to read Claude CLI stream: %w", scanErr)
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("Claude CLI failed: %s", normalizeClaudeCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String())))
	}

	finalContent := strings.TrimSpace(finalResult.Result)
	if finalContent == "" {
		finalContent = strings.TrimSpace(content.String())
	}
	if finalContent == "" {
		finalContent = strings.TrimSpace(assistantContent)
	}
	if finalContent == "" {
		finalContent = strings.TrimSpace(finalResult.Message)
	}
	if finalContent == "" && !sawResult {
		finalContent = strings.TrimSpace(stdout.String())
	}
	if finalResult.IsError || strings.Contains(strings.ToLower(finalResult.Subtype), "error") {
		msg := firstNonEmpty(finalContent, finalResult.Error, "Claude CLI returned an error result")
		return nil, fmt.Errorf("%s", normalizeClaudeCLIErrorMessage(msg))
	}

	if onEvent != nil {
		if err := onEvent(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: usage}); err != nil {
			return nil, err
		}
	}
	logging.LogResponseWithContent(usage.InputTokens, usage.OutputTokens, 0, finalContent, nil)
	return &llm.ChatResponse{
		Content:    finalContent,
		Usage:      usage,
		StopReason: firstNonEmpty(stopReason, finalResult.StopReason, finalResult.Subtype),
		ResponseID: firstNonEmpty(responseID, finalResult.SessionID),
	}, nil
}

func (c *Client) buildArgs(request *llm.ChatRequest, model, prompt string) []string {
	args := []string{"-p", prompt, "--output-format", "json", "--model", model}
	return c.appendCommonArgs(args, request)
}

func (c *Client) buildStreamArgs(request *llm.ChatRequest, model, prompt string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--model", model,
	}
	return c.appendCommonArgs(args, request)
}
func (c *Client) appendCommonArgs(args []string, request *llm.ChatRequest) []string {
	if systemPrompt := buildSystemPrompt(request.SystemPrompt); systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	if c.options.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	} else if sessionID := strings.TrimSpace(request.SessionID); isUUIDLike(sessionID) {
		args = append(args, "--session-id", sessionID)
	}
	// WHY: Claude CLI is itself the tool-running agent for Anthropic. A2gent
	// function schemas are not sent to the CLI, so expose Claude Code's native
	// tools explicitly and auto-allow only the capabilities that correspond to the
	// tools currently enabled in this A2gent request. This lets Sonnet edit files in
	// non-interactive mode without waiting for an invisible permission prompt.
	toolsArg, allowedArg, includeTools := claudeToolsArgs(request)
	if includeTools {
		args = append(args, "--tools", toolsArg)
		if allowedArg != "" {
			args = append(args, "--allowedTools", allowedArg)
		}
	}
	permissionMode := c.options.PermissionMode
	if permissionMode == "" {
		permissionMode = defaultPermissionMode
	}
	if permissionMode != "" {
		args = append(args, "--permission-mode", permissionMode)
	}
	if c.options.MaxBudgetUSD != "" {
		args = append(args, "--max-budget-usd", c.options.MaxBudgetUSD)
	}
	return args
}

var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
