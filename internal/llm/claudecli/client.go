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
		NoSessionPersistence: envBoolDefault("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", false),
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

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Claude CLI: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, llm.UnsafeForRetry(fmt.Errorf("Claude CLI failed: %s", normalizeClaudeCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String()))))
	}

	parsed, raw, err := parseCLIResult(stdout.String())
	if err != nil {
		return nil, llm.UnsafeForRetry(err)
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
		return nil, llm.UnsafeForRetry(fmt.Errorf("%s", normalizeClaudeCLIErrorMessage(msg)))
	}

	usage := usageFromRaw(parsed.Usage)
	logging.LogResponseWithContent(usage.InputTokens, usage.OutputTokens, 0, content, nil)
	return &llm.ChatResponse{
		Content:               content,
		Usage:                 usage,
		StopReason:            firstNonEmpty(parsed.StopReason, parsed.Subtype),
		ProviderSessionCursor: c.providerSessionCursor(parsed.SessionID),
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
	started := true
	unsafeAfterStart := func(err error) error {
		if err == nil || !started {
			return err
		}
		return llm.UnsafeForRetry(err)
	}

	processor := newStreamProcessor(onEvent)

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
			return nil, unsafeAfterStart(err)
		}
		if err := processor.handleEnvelope(event); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, unsafeAfterStart(err)
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, unsafeAfterStart(fmt.Errorf("failed to read Claude CLI stream: %w", scanErr))
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, unsafeAfterStart(fmt.Errorf("Claude CLI failed: %s", normalizeClaudeCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String()))))
	}

	finalContent := strings.TrimSpace(processor.finalResult.Result)
	if finalContent == "" {
		finalContent = strings.TrimSpace(processor.content.String())
	}
	if finalContent == "" {
		finalContent = strings.TrimSpace(processor.assistantContent)
	}
	if finalContent == "" {
		finalContent = strings.TrimSpace(processor.finalResult.Message)
	}
	if finalContent == "" && !processor.sawResult {
		finalContent = strings.TrimSpace(stdout.String())
	}
	if processor.finalResult.IsError || strings.Contains(strings.ToLower(processor.finalResult.Subtype), "error") {
		msg := firstNonEmpty(finalContent, processor.finalResult.Error, "Claude CLI returned an error result")
		return nil, unsafeAfterStart(fmt.Errorf("%s", normalizeClaudeCLIErrorMessage(msg)))
	}

	if err := processor.finalize(onEvent); err != nil {
		return nil, unsafeAfterStart(err)
	}
	logging.LogResponseWithContent(processor.usage.InputTokens, processor.usage.OutputTokens, 0, finalContent, nil)
	return &llm.ChatResponse{
		Content:               finalContent,
		Usage:                 processor.usage,
		StopReason:            firstNonEmpty(processor.stopReason, processor.finalResult.StopReason, processor.finalResult.Subtype),
		ProviderSessionCursor: c.providerSessionCursor(firstNonEmpty(processor.providerSessionCursor, processor.finalResult.SessionID)),
	}, nil
}

func (c *Client) providerSessionCursor(raw string) string {
	if c.options.NoSessionPersistence {
		return ""
	}
	return strings.TrimSpace(raw)
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
	} else if cursor := strings.TrimSpace(request.ProviderSessionCursor); cursor != "" {
		args = append(args, "--resume", cursor)
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
