package cursorcli

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
	defaultExecutable     = "agent"
	defaultModel          = "composer-2.5"
	defaultMaxOutputBytes = 4 * 1024 * 1024
	cursorPromptPrefix    = "You are running through Cursor Agent CLI. Use Cursor's native tools directly when you need to inspect or modify files, then return the final answer to A2gent. Do not print JSON tool calls for A2gent to execute."
)

// Options controls how the Cursor Agent CLI is invoked.
type Options struct {
	Executable string
	WorkDir    string
	Force      bool
	Sandbox    string
	APIKey     string
}

// Client implements llm.Client by shelling out to Cursor Agent CLI.
type Client struct {
	model   string
	options Options
}

type cliResult struct {
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype"`
	IsError       bool            `json:"is_error"`
	Result        string          `json:"result"`
	Text          string          `json:"text"`
	Message       string          `json:"message"`
	Error         string          `json:"error"`
	SessionID     string          `json:"session_id"`
	RequestID     string          `json:"request_id"`
	DurationMS    int64           `json:"duration_ms"`
	DurationAPIMS int64           `json:"duration_api_ms"`
	Usage         json.RawMessage `json:"usage"`
}

type cliStreamEnvelope struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	IsError     bool            `json:"is_error"`
	Result      string          `json:"result"`
	Text        string          `json:"text"`
	Message     json.RawMessage `json:"message"`
	Error       string          `json:"error"`
	SessionID   string          `json:"session_id"`
	RequestID   string          `json:"request_id"`
	Usage       json.RawMessage `json:"usage"`
	TimestampMS *int64          `json:"timestamp_ms"`
	ModelCallID string          `json:"model_call_id"`
	DurationMS  int64           `json:"duration_ms"`
}

type cliStreamMessage struct {
	Role    string             `json:"role"`
	Content []cliStreamContent `json:"content"`
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
		WorkDir: workDir,
		Force:   envBoolDefault("AAGENT_CURSOR_CLI_FORCE", false),
		Sandbox: strings.TrimSpace(os.Getenv("AAGENT_CURSOR_CLI_SANDBOX")),
		APIKey:  strings.TrimSpace(os.Getenv("CURSOR_API_KEY")),
	})
}

func NewClientWithOptions(model string, options Options) *Client {
	options.Executable = normalizeExecutable(options.Executable)
	options.WorkDir = normalizeWorkDir(options.WorkDir)
	options.Sandbox = strings.TrimSpace(options.Sandbox)
	options.APIKey = strings.TrimSpace(options.APIKey)
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
		model = defaultModel
	}

	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	prompt := buildPrompt(request)
	args := c.buildArgs(model, prompt)

	agentPath, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Cursor Agent CLI executable %q was not found in PATH; install Cursor CLI with `curl https://cursor.com/install -fsS | bash` or set AAGENT_CURSOR_CLI_PATH", c.options.Executable)
	}

	cmd := exec.CommandContext(ctx, agentPath, args...)
	cmd.Dir = c.options.WorkDir
	cmd.Env = c.commandEnv()

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
		return nil, fmt.Errorf("Cursor Agent CLI failed: %s", normalizeCursorCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String())))
	}

	parsed, raw, err := parseCLIResult(stdout.String())
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(firstNonEmpty(parsed.Result, parsed.Text, parsed.Message, raw))
	if parsed.IsError || strings.Contains(strings.ToLower(parsed.Subtype), "error") {
		msg := firstNonEmpty(content, parsed.Error, "Cursor Agent CLI returned an error result")
		return nil, fmt.Errorf("%s", normalizeCursorCLIErrorMessage(msg))
	}

	usage := usageFromRaw(parsed.Usage)
	logging.LogResponseWithContent(usage.InputTokens, usage.OutputTokens, 0, content, nil)
	return &llm.ChatResponse{
		Content:    content,
		Usage:      usage,
		StopReason: firstNonEmpty(parsed.Subtype, "success"),
		ResponseID: firstNonEmpty(strings.TrimSpace(parsed.SessionID), strings.TrimSpace(parsed.RequestID)),
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
		model = defaultModel
	}

	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	prompt := buildPrompt(request)
	args := c.buildStreamArgs(model, prompt)

	agentPath, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Cursor Agent CLI executable %q was not found in PATH; install Cursor CLI with `curl https://cursor.com/install -fsS | bash` or set AAGENT_CURSOR_CLI_PATH", c.options.Executable)
	}

	cmd := exec.CommandContext(ctx, agentPath, args...)
	cmd.Dir = c.options.WorkDir
	cmd.Env = c.commandEnv()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open Cursor Agent CLI stdout: %w", err)
	}
	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = defaultMaxOutputBytes
	stderr.limit = defaultMaxOutputBytes
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Cursor Agent CLI: %w", err)
	}

	var content strings.Builder
	var assistantContent string
	var usage llm.TokenUsage
	var responseID string
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
		if event.RequestID != "" && responseID == "" {
			responseID = event.RequestID
		}
		switch event.Type {
		case "assistant":
			text := streamEnvelopeMessageText(event)
			if text == "" {
				continue
			}
			assistantContent = text
			// WHY: Cursor stream-json emits duplicate assistant flushes around tool calls.
			// Only timestamped events without model_call_id are incremental deltas when
			// --stream-partial-output is enabled.
			isDelta := event.TimestampMS != nil && strings.TrimSpace(event.ModelCallID) == ""
			if isDelta {
				content.WriteString(text)
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{Type: llm.StreamEventContentDelta, ContentDelta: text}); err != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						return nil, err
					}
				}
			}
		case "result":
			sawResult = true
			finalResult = cliResult{
				Type:      event.Type,
				Subtype:   event.Subtype,
				IsError:   event.IsError,
				Result:    event.Result,
				Text:      event.Text,
				Error:     event.Error,
				SessionID: event.SessionID,
				RequestID: event.RequestID,
				Usage:     event.Usage,
			}
			if event.Usage != nil {
				usage = mergeUsage(usage, usageFromRaw(event.Usage))
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("failed to read Cursor Agent CLI stream: %w", scanErr)
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("Cursor Agent CLI failed: %s", normalizeCursorCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String())))
	}

	finalContent := strings.TrimSpace(firstNonEmpty(finalResult.Result, finalResult.Text))
	if finalContent == "" {
		finalContent = strings.TrimSpace(content.String())
	}
	if finalContent == "" {
		finalContent = strings.TrimSpace(assistantContent)
	}
	if finalContent == "" && !sawResult {
		finalContent = strings.TrimSpace(stdout.String())
	}
	if finalResult.IsError || strings.Contains(strings.ToLower(finalResult.Subtype), "error") {
		msg := firstNonEmpty(finalContent, finalResult.Error, "Cursor Agent CLI returned an error result")
		return nil, fmt.Errorf("%s", normalizeCursorCLIErrorMessage(msg))
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
		StopReason: firstNonEmpty(finalResult.Subtype, "success"),
		ResponseID: firstNonEmpty(responseID, finalResult.SessionID, finalResult.RequestID),
	}, nil
}

func (c *Client) buildArgs(model, prompt string) []string {
	args := []string{"-p", prompt, "--output-format", "json", "--model", model}
	return c.appendCommonArgs(args)
}

func (c *Client) buildStreamArgs(model, prompt string) []string {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--stream-partial-output", "--model", model}
	return c.appendCommonArgs(args)
}

func (c *Client) appendCommonArgs(args []string) []string {
	args = append(args, "--workspace", c.options.WorkDir)
	if c.options.Force {
		args = append(args, "--force")
	}
	if c.options.Sandbox != "" {
		args = append(args, "--sandbox", c.options.Sandbox)
	}
	if c.options.APIKey != "" {
		args = append(args, "--api-key", c.options.APIKey)
	}
	return args
}

func (c *Client) commandEnv() []string {
	if c.options.APIKey == "" {
		return os.Environ()
	}
	env := os.Environ()
	for i, item := range env {
		if strings.HasPrefix(item, "CURSOR_API_KEY=") {
			env[i] = "CURSOR_API_KEY=" + c.options.APIKey
			return env
		}
	}
	return append(env, "CURSOR_API_KEY="+c.options.APIKey)
}

func buildSystemPrompt(systemPrompt string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	if systemPrompt == "" {
		return cursorPromptPrefix
	}
	return cursorPromptPrefix + "\n\n" + systemPrompt
}

func buildPrompt(request *llm.ChatRequest) string {
	if request == nil || len(request.Messages) == 0 {
		return buildSystemPrompt("") + "\n\nContinue."
	}
	if len(request.Messages) == 1 && request.Messages[0].Role == "user" &&
		len(request.Messages[0].ToolCalls) == 0 && len(request.Messages[0].ToolResults) == 0 &&
		len(request.Messages[0].Images) == 0 {
		prompt := strings.TrimSpace(request.Messages[0].Content)
		if systemPrompt := buildSystemPrompt(request.SystemPrompt); systemPrompt != "" {
			return strings.TrimSpace(systemPrompt + "\n\n" + prompt)
		}
		return prompt
	}

	var b strings.Builder
	if systemPrompt := buildSystemPrompt(request.SystemPrompt); systemPrompt != "" {
		b.WriteString(systemPrompt)
		b.WriteString("\n\n")
	}
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
		b.WriteString("[Image attachment omitted in Cursor CLI provider adapter: ")
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
		return cliResult{}, "", fmt.Errorf("Cursor Agent CLI returned empty output")
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
		return cliStreamEnvelope{}, fmt.Errorf("failed to parse Cursor Agent CLI stream line: %w", err)
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

func streamEnvelopeMessageText(event cliStreamEnvelope) string {
	raw := bytes.TrimSpace(event.Message)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return strings.TrimSpace(text)
		}
	}
	var message cliStreamMessage
	if err := json.Unmarshal(raw, &message); err == nil {
		return streamMessageText(message)
	}
	return ""
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
		InputTokens:       intFromMap(values, "inputTokens") + intFromMap(values, "input_tokens"),
		OutputTokens:      intFromMap(values, "outputTokens") + intFromMap(values, "output_tokens"),
		CachedInputTokens: intFromMap(values, "cacheReadTokens") + intFromMap(values, "cache_read_tokens") + intFromMap(values, "cached_input_tokens"),
		ReasoningTokens:   intFromMap(values, "reasoningTokens") + intFromMap(values, "reasoning_tokens"),
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
	return firstNonEmpty(parsed.Error, parsed.Result, parsed.Text, parsed.Message)
}

func cliStreamEnvelopeMessage(event cliStreamEnvelope) string {
	return firstNonEmpty(event.Error, event.Result, event.Text, streamEnvelopeMessageText(event))
}

func normalizeCursorCLIErrorMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return "Cursor Agent CLI returned an error without details"
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "a2gent hint:") {
		return msg
	}

	switch {
	case isCursorCLIRateLimitError(lower):
		return msg + "\nA2gent hint: Cursor Agent CLI hit a rate limit. Wait for the limit window to reset, lower concurrency, or switch the session/provider to a fallback model."
	case isCursorCLICreditsError(lower):
		return msg + "\nA2gent hint: Cursor Agent CLI reported a credits, quota, usage, or billing problem. Check Cursor plan usage and billing."
	case isCursorCLIPermissionError(lower):
		return msg + "\nA2gent hint: Cursor Agent CLI could not proceed because a tool permission was denied or required an interactive prompt. Configure .cursor/cli.json permissions or opt in to AAGENT_CURSOR_CLI_FORCE=true for trusted workspaces."
	case isCursorCLIAuthError(lower):
		return msg + "\nA2gent hint: Cursor Agent CLI authentication is not ready. Run `agent login` locally or set CURSOR_API_KEY."
	default:
		return msg
	}
}

func isCursorCLIRateLimitError(lower string) bool {
	return strings.Contains(lower, "rate limit") || strings.Contains(lower, "ratelimit") || strings.Contains(lower, "too many requests") || strings.Contains(lower, "429")
}

func isCursorCLICreditsError(lower string) bool {
	return strings.Contains(lower, "out of credits") || strings.Contains(lower, "no credits") || strings.Contains(lower, "insufficient credits") || strings.Contains(lower, "usage limit") || strings.Contains(lower, "quota") || strings.Contains(lower, "billing") || strings.Contains(lower, "payment required") || strings.Contains(lower, "402")
}

func isCursorCLIPermissionError(lower string) bool {
	return strings.Contains(lower, "permission denied") || strings.Contains(lower, "requires permission") || strings.Contains(lower, "permission prompt") || strings.Contains(lower, "not allowed") || strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "confirmation")
}

func isCursorCLIAuthError(lower string) bool {
	return strings.Contains(lower, "not authenticated") || strings.Contains(lower, "not logged in") || strings.Contains(lower, "login required") || strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "401")
}

func normalizeExecutable(raw string) string {
	if path := strings.TrimSpace(os.Getenv("AAGENT_CURSOR_CLI_PATH")); path != "" {
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
		paths = append([]string{filepath.Join(home, ".local", "bin", executable)}, paths...)
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
	raw = strings.TrimPrefix(raw, "cursor/")
	if raw == "composer-2" || raw == "composer-2-fast" {
		return defaultModel
	}
	return strings.TrimSpace(raw)
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
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
