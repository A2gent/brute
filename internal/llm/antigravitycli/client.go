package antigravitycli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	defaultExecutable     = "agy"
	defaultMaxOutputBytes = 4 * 1024 * 1024
	maxToolOutputBytes    = 16 * 1024
	InstallCommand        = "curl -fsSL https://antigravity.google/cli/install.sh | bash"
	commandWaitDelay      = 5 * time.Second
)

var ErrImagesUnsupported = errors.New("image attachments are not supported in Antigravity mode")

type Options struct {
	Executable      string
	WorkDir         string
	SkipPermissions bool
}

type Client struct {
	model   string
	options Options
}

type streamEvent struct {
	Event      string      `json:"event"`
	Init       *initInfo   `json:"init"`
	StepUpdate *stepUpdate `json:"step_update"`
	Result     *resultInfo `json:"result"`
}

type initInfo struct {
	Model string `json:"model"`
}

type stepUpdate struct {
	ConversationID string     `json:"conversation_id"`
	StepIndex      int        `json:"step_index"`
	State          string     `json:"state"`
	StepType       string     `json:"step_type"`
	TextDelta      string     `json:"text_delta"`
	ThinkingDelta  string     `json:"thinking_delta"`
	ToolName       string     `json:"tool_name"`
	ToolInfo       *toolInfo  `json:"tool_info"`
	Usage          *usageInfo `json:"usage"`
}

type toolInfo struct {
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
	Output     string          `json:"output"`
	Error      string          `json:"error"`
}

type resultInfo struct {
	ConversationID string     `json:"conversation_id"`
	Status         string     `json:"status"`
	Response       string     `json:"response"`
	Error          string     `json:"error"`
	FinishedReason string     `json:"finished_reason"`
	Usage          *usageInfo `json:"usage"`
}

type usageInfo struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
}

func (u *usageInfo) tokenUsage() llm.TokenUsage {
	if u == nil {
		return llm.TokenUsage{}
	}
	return llm.TokenUsage{
		InputTokens:       u.InputTokens + u.CacheReadTokens,
		OutputTokens:      u.OutputTokens,
		CachedInputTokens: u.CacheReadTokens,
		ReasoningTokens:   u.ThinkingTokens,
	}
}

func NewClientWithOptions(model string, options Options) *Client {
	options.Executable = normalizeExecutable(options.Executable)
	options.WorkDir = normalizeWorkDir(options.WorkDir)
	options.SkipPermissions = options.SkipPermissions || envBoolDefault("AAGENT_ANTIGRAVITY_CLI_SKIP_PERMISSIONS", true)
	return &Client{model: NormalizeModel(model), options: options}
}

func IsAvailable() bool {
	_, err := ResolveExecutable("")
	return err == nil
}

func ResolveExecutable(raw string) (string, error) {
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

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, request, nil)
}

func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	if request == nil {
		request = &llm.ChatRequest{}
	}
	model := c.model
	if requested := strings.TrimSpace(request.Model); requested != "" {
		model = NormalizeModel(requested)
	}
	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	agyPath, err := ResolveExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Antigravity CLI executable %q was not found; install it with `%s` or set AAGENT_ANTIGRAVITY_CLI_PATH", c.options.Executable, InstallCommand)
	}
	if last := request.Messages; len(last) > 0 && len(last[len(last)-1].Images) > 0 {
		return nil, ErrImagesUnsupported
	}
	cmd := exec.CommandContext(ctx, agyPath, c.buildArgs(model, buildPrompt(request))...)
	cmd.Dir = c.options.WorkDir
	cmd.WaitDelay = commandWaitDelay
	configureCommand(cmd)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open Antigravity CLI stdout: %w", err)
	}
	stderr := &limitedBuffer{limit: defaultMaxOutputBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Antigravity CLI: %w", err)
	}

	state := newStreamState(onEvent)
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 64*1024), defaultMaxOutputBytes)
	for scanner.Scan() {
		if err := state.handleLine(scanner.Bytes()); err != nil {
			killCommand(cmd)
			_ = cmd.Wait()
			return nil, err
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		killCommand(cmd)
		_ = cmd.Wait()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("failed to read Antigravity CLI stream: %w", scanErr)
	}
	waitErr := cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return state.finish(waitErr, stderr.String())
}

func (c *Client) buildArgs(model, prompt string) []string {
	args := []string{"--print", prompt, "--output-format", "stream-json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if c.options.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}

type streamState struct {
	emit       func(llm.StreamEvent) error
	content    strings.Builder
	result     *resultInfo
	usage      llm.TokenUsage
	responseID string
	model      string
	tools      map[int]int
	plainLines []string
}

func newStreamState(onEvent func(llm.StreamEvent) error) *streamState {
	return &streamState{emit: onEvent, tools: map[int]int{}}
}

func (s *streamState) send(event llm.StreamEvent) error {
	if s.emit == nil {
		return nil
	}
	return s.emit(event)
}

func (s *streamState) handleLine(line []byte) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var event streamEvent
	if line[0] != '{' || json.Unmarshal(line, &event) != nil {
		s.plainLines = append(s.plainLines, string(line))
		return nil
	}
	switch {
	case event.Init != nil:
		s.model = strings.TrimSpace(event.Init.Model)
	case event.StepUpdate != nil:
		return s.handleStep(event.StepUpdate)
	case event.Result != nil:
		s.result = event.Result
		if event.Result.ConversationID != "" {
			s.responseID = event.Result.ConversationID
		}
	}
	return nil
}

func (s *streamState) handleStep(step *stepUpdate) error {
	if step.ConversationID != "" {
		s.responseID = step.ConversationID
	}
	switch step.StepType {
	case "agent_response":
		if step.ThinkingDelta != "" {
			if err := s.send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: step.ThinkingDelta}); err != nil {
				return err
			}
		}
		if step.TextDelta != "" {
			s.content.WriteString(step.TextDelta)
			return s.send(llm.StreamEvent{Type: llm.StreamEventContentDelta, ContentDelta: step.TextDelta})
		}
	case "tool":
		return s.handleTool(step)
	}
	return nil
}

func (s *streamState) handleTool(step *stepUpdate) error {
	name := strings.TrimSpace(step.ToolName)
	input := ""
	if step.ToolInfo != nil {
		name = firstNonEmpty(name, step.ToolInfo.Name)
		input = string(step.ToolInfo.Parameters)
	}
	id := fmt.Sprintf("agy_step_%d", step.StepIndex)
	index, started := s.tools[step.StepIndex]
	if !started {
		index = len(s.tools)
		s.tools[step.StepIndex] = index
		if err := s.send(llm.StreamEvent{Type: llm.StreamEventToolStarted, ToolCallIndex: index, ToolCallID: id, ToolCallName: name}); err != nil {
			return err
		}
	}
	if step.State != "DONE" && step.State != "ERROR" {
		return nil
	}
	output, isErr := "", step.State == "ERROR"
	if step.ToolInfo != nil {
		output = step.ToolInfo.Output
		if step.ToolInfo.Error != "" {
			output, isErr = step.ToolInfo.Error, true
		}
	}
	if len(output) > maxToolOutputBytes {
		output = output[:maxToolOutputBytes] + "\n...[truncated]"
	}
	if err := s.send(llm.StreamEvent{Type: llm.StreamEventToolOutput, ToolCallID: id, ToolCallName: name, ToolOutput: output, ToolIsError: isErr}); err != nil {
		return err
	}
	return s.send(llm.StreamEvent{Type: llm.StreamEventToolCompleted, ToolCallIndex: index, ToolCallID: id, ToolCallName: name, ToolInputDelta: input})
}

func (s *streamState) finish(waitErr error, stderr string) (*llm.ChatResponse, error) {
	if s.result != nil && !strings.EqualFold(s.result.Status, "SUCCESS") {
		return nil, fmt.Errorf("Antigravity CLI failed: %s", normalizeErrorMessage(firstNonEmpty(s.result.Error, stderr, strings.Join(s.plainLines, "\n"), s.result.Status)))
	}
	if waitErr != nil {
		return nil, fmt.Errorf("Antigravity CLI failed: %s", normalizeErrorMessage(firstNonEmpty(stderr, strings.Join(s.plainLines, "\n"), waitErr.Error())))
	}
	if s.result == nil {
		return nil, fmt.Errorf("Antigravity CLI ended without a result: %s", normalizeErrorMessage(firstNonEmpty(stderr, strings.Join(s.plainLines, "\n"), "no output")))
	}
	content := strings.TrimSpace(firstNonEmpty(s.result.Response, s.content.String()))
	s.usage = s.result.Usage.tokenUsage()
	if len(s.tools) > 0 {
		s.usage.ContextTokens = -1
	}
	if err := s.send(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: s.usage}); err != nil {
		return nil, err
	}
	logging.LogResponseWithContent(s.usage.InputTokens, s.usage.OutputTokens, 0, content, nil)
	return &llm.ChatResponse{
		Model:      s.model,
		Content:    content,
		Usage:      s.usage,
		StopReason: firstNonEmpty(strings.ToLower(s.result.FinishedReason), "success"),
		ResponseID: s.responseID,
	}, nil
}

func normalizeErrorMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return "Antigravity CLI returned an error without details"
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "authentication") || strings.Contains(lower, "not signed in") || strings.Contains(lower, "login"):
		return msg + "\nA2gent hint: Antigravity CLI is not signed in. Run `agy` once in a terminal and complete Google Sign-In."
	case strings.Contains(lower, "quota") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "429"):
		return msg + "\nA2gent hint: Antigravity reported a quota or rate limit. Wait for the limit to reset or switch the model."
	default:
		return msg
	}
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return strings.TrimSpace(b.buf.String())
}

func normalizeExecutable(raw string) string {
	if path := strings.TrimSpace(os.Getenv("AAGENT_ANTIGRAVITY_CLI_PATH")); path != "" {
		return path
	}
	if raw = strings.TrimSpace(raw); raw != "" {
		return raw
	}
	return defaultExecutable
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

func envBoolDefault(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
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
