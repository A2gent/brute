package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

type cliResult struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	IsError        bool            `json:"is_error"`
	Result         string          `json:"result"`
	Message        string          `json:"message"`
	Error          string          `json:"error"`
	SessionID      string          `json:"session_id"`
	StopReason     string          `json:"stop_reason"`
	TotalCostUSD   float64         `json:"total_cost_usd"`
	DurationMS     int64           `json:"duration_ms"`
	DurationAPIMS  int64           `json:"duration_api_ms"`
	NumTurns       int             `json:"num_turns"`
	Usage          json.RawMessage `json:"usage"`
	PermissionMode string          `json:"permission_mode"`
}

type cliStreamEnvelope struct {
	Type       string           `json:"type"`
	Subtype    string           `json:"subtype"`
	IsError    bool             `json:"is_error"`
	Result     string           `json:"result"`
	Message    cliStreamMessage `json:"message"`
	Error      string           `json:"error"`
	SessionID  string           `json:"session_id"`
	StopReason string           `json:"stop_reason"`
	Usage      json.RawMessage  `json:"usage"`
	Event      cliStreamEvent   `json:"event"`
}

type cliStreamEvent struct {
	Type    string           `json:"type"`
	Delta   cliStreamDelta   `json:"delta"`
	Message cliStreamMessage `json:"message"`
	Usage   json.RawMessage  `json:"usage"`
}

type cliStreamDelta struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	StopReason string `json:"stop_reason"`
}

type cliStreamMessage struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	Content    []cliStreamContent `json:"content"`
	Usage      json.RawMessage    `json:"usage"`
	StopReason string             `json:"stop_reason"`
}

type cliStreamContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = b.buf.Write(p)
		} else {
			_, _ = b.buf.Write(p[:remaining])
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
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

func buildSystemPrompt(systemPrompt string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	if systemPrompt == "" {
		return claudeCodePromptPrefix
	}
	return claudeCodePromptPrefix + "\n\n" + systemPrompt
}

func buildPrompt(request *llm.ChatRequest) string {
	if request == nil || len(request.Messages) == 0 {
		return "Continue."
	}
	if len(request.Messages) == 1 && request.Messages[0].Role == "user" &&
		len(request.Messages[0].ToolCalls) == 0 && len(request.Messages[0].ToolResults) == 0 &&
		len(request.Messages[0].Images) == 0 {
		return strings.TrimSpace(request.Messages[0].Content)
	}

	var b strings.Builder
	b.WriteString("Continue the following A2gent conversation. Treat the final user message as the active request.\n\n")
	for _, msg := range request.Messages {
		writeMessage(&b, msg)
	}
	return strings.TrimSpace(b.String())
}

func writeMessage(b *strings.Builder, msg llm.Message) {
	role := strings.TrimSpace(msg.Role)
	if role == "" {
		role = "message"
	}
	b.WriteString(strings.ToUpper(role[:1]))
	if len(role) > 1 {
		b.WriteString(role[1:])
	}
	b.WriteString(":\n")
	if content := strings.TrimSpace(msg.Content); content != "" {
		b.WriteString(content)
		b.WriteString("\n")
	}
	for _, img := range msg.Images {
		label := strings.TrimSpace(img.Name)
		if label == "" {
			label = strings.TrimSpace(img.URL)
		}
		if label == "" {
			label = strings.TrimSpace(img.MediaType)
		}
		if label == "" {
			label = "inline image"
		}
		b.WriteString("[Image attachment omitted in Claude CLI provider adapter: ")
		b.WriteString(label)
		b.WriteString("]\n")
	}
	for _, tc := range msg.ToolCalls {
		b.WriteString("[Tool call: ")
		b.WriteString(tc.Name)
		if tc.ID != "" {
			b.WriteString(" id=")
			b.WriteString(tc.ID)
		}
		b.WriteString("]\n")
		if input := strings.TrimSpace(tc.Input); input != "" {
			b.WriteString(input)
			b.WriteString("\n")
		}
	}
	for _, tr := range msg.ToolResults {
		b.WriteString("[Tool result")
		if tr.Name != "" {
			b.WriteString(": ")
			b.WriteString(tr.Name)
		}
		if tr.ToolCallID != "" {
			b.WriteString(" id=")
			b.WriteString(tr.ToolCallID)
		}
		if tr.IsError {
			b.WriteString(" error=true")
		}
		b.WriteString("]\n")
		if tr.Content != "" {
			b.WriteString(tr.Content)
			if !strings.HasSuffix(tr.Content, "\n") {
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")
}

func parseCLIResult(stdout string) (cliResult, string, error) {
	raw := strings.TrimSpace(stdout)
	if raw == "" {
		return cliResult{}, "", fmt.Errorf("Claude CLI returned empty output")
	}

	var parsed cliResult
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return cliResult{Result: raw}, raw, nil
	}
	return parsed, raw, nil
}

func parseCLIStreamEnvelope(line string) (cliStreamEnvelope, error) {
	var event cliStreamEnvelope
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return cliStreamEnvelope{}, fmt.Errorf("failed to parse Claude CLI stream line: %w", err)
	}
	return event, nil
}

func streamMessageText(message cliStreamMessage) string {
	var b strings.Builder
	for _, item := range message.Content {
		if item.Type == "text" && item.Text != "" {
			b.WriteString(item.Text)
		}
	}
	return b.String()
}

func mergeUsage(current, next llm.TokenUsage) llm.TokenUsage {
	if next.InputTokens != 0 {
		current.InputTokens = next.InputTokens
	}
	if next.OutputTokens != 0 {
		current.OutputTokens = next.OutputTokens
	}
	if next.CachedInputTokens != 0 {
		current.CachedInputTokens = next.CachedInputTokens
	}
	if next.ReasoningTokens != 0 {
		current.ReasoningTokens = next.ReasoningTokens
	}
	return current
}

func usageFromRaw(raw json.RawMessage) llm.TokenUsage {
	if len(raw) == 0 {
		return llm.TokenUsage{}
	}
	var values map[string]interface{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return llm.TokenUsage{}
	}
	return llm.TokenUsage{
		InputTokens:       intFromMap(values, "input_tokens"),
		OutputTokens:      intFromMap(values, "output_tokens"),
		CachedInputTokens: intFromMap(values, "cache_read_input_tokens") + intFromMap(values, "cache_creation_input_tokens") + intFromMap(values, "cached_input_tokens"),
		ReasoningTokens:   intFromMap(values, "reasoning_tokens"),
	}
}

func intFromMap(values map[string]interface{}, key string) int {
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(value))
		return n
	default:
		return 0
	}
}

func cliErrorMessage(runErr error, stdout, stderr string) string {
	parts := make([]string, 0, 3)
	if stderr = strings.TrimSpace(stderr); stderr != "" {
		parts = append(parts, stderr)
	}
	if stdout = strings.TrimSpace(stdout); stdout != "" {
		if msg := cliOutputMessage(stdout); msg != "" {
			parts = append(parts, msg)
		} else {
			parts = append(parts, stdout)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, runErr.Error())
	}
	return strings.Join(parts, "\n")
}

func cliOutputMessage(stdout string) string {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return ""
	}
	if json.Valid([]byte(stdout)) {
		if parsed, _, err := parseCLIResult(stdout); err == nil {
			return cliResultMessage(parsed)
		}
	}

	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if event, err := parseCLIStreamEnvelope(line); err == nil {
			if msg := cliStreamEnvelopeMessage(event); msg != "" {
				return msg
			}
		}
		if parsed, _, err := parseCLIResult(line); err == nil {
			if msg := cliResultMessage(parsed); msg != "" {
				return msg
			}
		}
	}
	return ""
}

func cliResultMessage(parsed cliResult) string {
	return firstNonEmpty(parsed.Error, parsed.Result, parsed.Message)
}

func cliStreamEnvelopeMessage(event cliStreamEnvelope) string {
	return firstNonEmpty(event.Error, event.Result, streamMessageText(event.Message))
}

func normalizeClaudeCLIErrorMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return "Claude CLI returned an error without details"
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "a2gent hint:") {
		return msg
	}

	switch {
	case isClaudeCLIRateLimitError(lower):
		return msg + "\nA2gent hint: Claude CLI hit a rate limit. Wait for the limit window to reset, lower concurrency, or switch the session/provider to a fallback model."
	case isClaudeCLICreditsError(lower):
		return msg + "\nA2gent hint: Claude CLI reported a credits, quota, billing, or budget problem. Check Claude billing/plan credits and AAGENT_CLAUDE_CLI_MAX_BUDGET_USD."
	case isClaudeCLIPermissionError(lower):
		return msg + "\nA2gent hint: Claude CLI could not proceed because a tool permission was denied or required an interactive prompt. Use a non-interactive permission mode such as AAGENT_CLAUDE_CLI_PERMISSION_MODE=acceptEdits or allow the needed Claude Code tools."
	case isClaudeCLIAuthError(lower):
		return msg + "\nA2gent hint: Claude CLI authentication is not ready. Run Claude Code locally to sign in, or check the account/token used by the Claude CLI."
	default:
		return msg
	}
}

func isClaudeCLIRateLimitError(lower string) bool {
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "ratelimit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "429")
}

func isClaudeCLICreditsError(lower string) bool {
	return strings.Contains(lower, "out of credits") ||
		strings.Contains(lower, "no credits") ||
		strings.Contains(lower, "insufficient credits") ||
		strings.Contains(lower, "credit balance") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "billing") ||
		strings.Contains(lower, "payment required") ||
		strings.Contains(lower, "402") ||
		strings.Contains(lower, "max budget")
}

func isClaudeCLIPermissionError(lower string) bool {
	return strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "requires permission") ||
		strings.Contains(lower, "permission prompt") ||
		strings.Contains(lower, "tool use rejected") ||
		strings.Contains(lower, "not allowed to use") ||
		strings.Contains(lower, "operation not permitted")
}

func isClaudeCLIAuthError(lower string) bool {
	return strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "login required") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "401")
}

func normalizeExecutable(raw string) string {
	if path := strings.TrimSpace(os.Getenv("AAGENT_CLAUDE_CLI_PATH")); path != "" {
		return path
	}
	if raw = strings.TrimSpace(raw); raw != "" {
		return raw
	}
	return defaultExecutable
}

func findExecutable(raw string) (string, error) {
	executable := normalizeExecutable(raw)
	if strings.Contains(executable, string(os.PathSeparator)) {
		if isExecutableFile(executable) {
			return executable, nil
		}
		return "", os.ErrNotExist
	}
	if path, err := exec.LookPath(executable); err == nil {
		return path, nil
	}
	for _, candidate := range commonExecutablePaths(executable) {
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func commonExecutablePaths(executable string) []string {
	paths := []string{
		filepath.Join("/usr/local/bin", executable),
		filepath.Join("/opt/homebrew/bin", executable),
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append([]string{
			filepath.Join(home, ".local", "bin", executable),
		}, paths...)
	}
	return paths
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}
func claudeToolsArgs(request *llm.ChatRequest) (string, string, bool) {
	if request == nil {
		return "", "", true
	}
	toolNames := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		name := strings.TrimSpace(tool.Name)
		if name != "" {
			toolNames[name] = struct{}{}
		}
	}
	if len(toolNames) == 0 {
		return "", "", true
	}

	// Map A2gent tool availability to Claude Code's native tool names. We do not
	// include web/notification/sub-agent tools because Claude CLI cannot execute
	// A2gent server-backed integrations; those remain available through other
	// providers that support A2gent tool calls.
	allowed := make([]string, 0, 10)
	if hasAnyTool(toolNames, "bash") {
		allowed = append(allowed, "Bash")
	}
	if hasAnyTool(toolNames, "read", "grep", "glob", "find_files", "filter") {
		allowed = append(allowed, "Glob", "Grep", "LS", "Read")
	}
	if hasAnyTool(toolNames, "edit", "replace_lines", "insert_lines") {
		allowed = append(allowed, "Edit", "MultiEdit")
	}
	if hasAnyTool(toolNames, "write") {
		allowed = append(allowed, "Write")
	}
	if len(allowed) == 0 {
		return "", "", true
	}
	allowed = uniqueSorted(allowed)
	joined := strings.Join(allowed, ",")
	return joined, joined, true
}

func hasAnyTool(names map[string]struct{}, candidates ...string) bool {
	for _, candidate := range candidates {
		if _, ok := names[candidate]; ok {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	// Keep output deterministic for tests and easier CLI debugging.
	order := map[string]int{"Bash": 1, "Edit": 2, "Glob": 3, "Grep": 4, "LS": 5, "MultiEdit": 6, "Read": 7, "Write": 8}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if order[out[j]] < order[out[i]] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func normalizeWorkDir(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "."
	}
	if abs, err := filepath.Abs(raw); err == nil {
		return abs
	}
	return raw
}

func normalizeModel(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "anthropic/")
	return strings.TrimSpace(raw)
}

func isUUIDLike(raw string) bool {
	if len(raw) != 36 {
		return false
	}
	for i, ch := range raw {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
	}
	return true
}

func envBoolDefault(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
