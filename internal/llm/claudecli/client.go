package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/approval"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	defaultExecutable      = "claude"
	defaultMaxOutputBytes  = 4 * 1024 * 1024
	defaultPermissionMode  = "acceptEdits"
	claudeCodePromptPrefix = "You are running through Claude Code CLI. Use Claude Code's native tools directly when you need to inspect or modify files, then return the final answer to A2gent. Use the a2gent MCP tools when available for integrations and delegation (mcp__a2gent__delegate_to_agent). Include relevant context, file paths, and the expected deliverable in the task; the child does not inherit this conversation. Read its returned response before continuing. Do not print JSON tool calls for A2gent to execute."
)

// MCPBridgeHook builds the per-process MCP bridge configuration for a
// session-scoped CLI run. It returns the inline --mcp-config JSON and a revoke
// callback tied to the CLI process lifetime; an empty configJSON disables the
// bridge for that invocation.
type MCPBridgeHook func(ctx context.Context, sessionID string) (configJSON string, revoke func(), err error)

// Options controls how the Claude Code CLI is invoked.
type Options struct {
	Executable           string
	WorkDir              string
	ConfigDir            string
	HomePath             string
	Environment          []string
	Identity             string
	PermissionMode       string
	MaxBudgetUSD         string
	NoSessionPersistence bool
	// MCPOnly disables native tools and requires the session-scoped MCP bridge.
	MCPOnly bool

	// Persistent keeps one streaming-input CLI process per session; budgets
	// remain per-spawn. On by default, AAGENT_CLAUDE_CLI_PERSISTENT=0 disables it.
	Persistent             bool
	PersistentIdleTimeout  time.Duration
	PersistentMaxProcesses int

	// MCPBridge exposes A2gent tools (question, integrations) to the CLI over a
	// loopback MCP server; only used on the direct (non-sidecar) path.
	MCPBridge MCPBridgeHook
	// Stable endpoint/config generation, excluding the per-process bearer token.
	MCPBridgeGeneration string

	// Optional Claude Agent SDK sidecar transport (enabled only when
	// AAGENT_CLAUDE_AGENT_SDK_SIDECAR_PATH is set and Broker is non-nil).
	Broker               *approval.Broker
	SidecarPath          string
	NodePath             string
	ApprovalTimeout      time.Duration
	TakeApprovalResponse func(requestID string) (ApprovalResolvePayload, bool)
}

// ApprovalResolvePayload carries user answers submitted via HTTP approval resolve.
type ApprovalResolvePayload struct {
	Answers map[string]string
	Message string
}

// mcpBridgeInvocation carries the per-run MCP bridge CLI args and the token
// revocation callback that must run when the CLI subprocess exits.
type mcpBridgeInvocation struct {
	args         []string
	allowedTools string
	revoke       func()
	err          error
}

func (c *Client) newMCPBridgeInvocation(ctx context.Context) mcpBridgeInvocation {
	inv := mcpBridgeInvocation{revoke: func() {}}
	if c.options.MCPBridge == nil {
		if c.options.MCPOnly {
			inv.err = fmt.Errorf("MCP-only invocation requires an MCP bridge")
		}
		return inv
	}
	sessionID, _ := ctx.Value("session_id").(string)
	if strings.TrimSpace(sessionID) == "" {
		if c.options.MCPOnly {
			inv.err = fmt.Errorf("MCP-only invocation requires a session context")
		}
		return inv
	}
	configJSON, revoke, err := c.options.MCPBridge(ctx, sessionID)
	if err != nil {
		if revoke != nil {
			revoke()
		}
		inv.err = err
		if !c.options.MCPOnly {
			logging.Warn("MCP bridge config failed, continuing without bridge: %v", err)
		}
		return inv
	}
	if strings.TrimSpace(configJSON) == "" {
		if c.options.MCPOnly {
			inv.err = fmt.Errorf("MCP-only invocation requires a non-empty MCP bridge config")
		}
		if revoke != nil {
			revoke()
		}
		return inv
	}
	if revoke != nil {
		inv.revoke = revoke
	}
	inv.args = []string{"--mcp-config", configJSON, "--strict-mcp-config"}
	inv.allowedTools = "mcp__a2gent__*"
	return inv
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
	options.Persistent = options.Persistent || envBoolDefault("AAGENT_CLAUDE_CLI_PERSISTENT", true)
	if options.MCPOnly {
		options.NoSessionPersistence = true
		options.Persistent = false
		options.SidecarPath = ""
		options.Broker = nil
	}
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
	c = c.forInvocation(ctx)
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

	if request.SessionID != "" && (!c.persistentEnabled(request) || SidecarEnabled(c.options)) {
		closePersistentSession(c.options, request.SessionID)
	}
	if SidecarEnabled(c.options) {
		return c.chatViaSidecar(ctx, request, model, nil)
	}

	if c.persistentEnabled(request) || hasInlineImages(request) {
		// WHY: --input-format stream-json (needed for image blocks) requires stream-json output.
		return c.ChatStream(ctx, request, func(llm.StreamEvent) error { return nil })
	}

	prompt := buildPrompt(request)
	bridge := c.newMCPBridgeInvocation(ctx)
	defer bridge.revoke()
	if c.options.MCPOnly && bridge.err != nil {
		return nil, fmt.Errorf("prepare MCP-only invocation: %w", bridge.err)
	}
	args := c.buildArgs(request, model, prompt, bridge)

	claudePath, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Claude CLI executable %q was not found in PATH; install Claude Code or set AAGENT_CLAUDE_CLI_PATH", c.options.Executable)
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = c.options.WorkDir
	cmd.Env = c.commandEnv()
	configureOwnedCommand(cmd)

	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = defaultMaxOutputBytes
	stderr.limit = defaultMaxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Claude CLI: %w", err)
	}
	defer guardProcessGroup(cmd).release()
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
	if parsed.NumTurns > 1 {
		// JSON output contains only aggregate usage, not the final turn's input.
		usage.ContextTokens = -1
	}
	logging.LogResponseWithContent(usage.InputTokens, usage.OutputTokens, 0, content, nil)
	return &llm.ChatResponse{
		Content:               content,
		Usage:                 usage,
		StopReason:            firstNonEmpty(parsed.StopReason, parsed.Subtype),
		ProviderSessionCursor: c.providerSessionCursor(parsed.SessionID),
	}, nil
}

func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	c = c.forInvocation(ctx)
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

	if request.SessionID != "" && (!c.persistentEnabled(request) || SidecarEnabled(c.options)) {
		closePersistentSession(c.options, request.SessionID)
	}
	if SidecarEnabled(c.options) {
		return c.chatViaSidecar(ctx, request, model, onEvent)
	}

	if c.persistentEnabled(request) {
		if resp, err, handled := c.chatPersistent(ctx, request, model, onEvent); handled {
			return resp, err
		}
	}

	prompt := buildPrompt(request)
	bridge := c.newMCPBridgeInvocation(ctx)
	defer bridge.revoke()
	if c.options.MCPOnly && bridge.err != nil {
		return nil, fmt.Errorf("prepare MCP-only invocation: %w", bridge.err)
	}
	args := c.buildStreamArgs(request, model, prompt, bridge)
	var stdinPayload []byte
	if hasInlineImages(request) {
		payload, err := buildStreamInput(request, prompt)
		if err != nil {
			return nil, fmt.Errorf("failed to encode Claude CLI image input: %w", err)
		}
		stdinPayload = payload
		args = withStdinPrompt(args)
	}

	claudePath, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Claude CLI executable %q was not found in PATH; install Claude Code or set AAGENT_CLAUDE_CLI_PATH", c.options.Executable)
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = c.options.WorkDir
	cmd.Env = c.commandEnv()
	configureOwnedCommand(cmd)
	if stdinPayload != nil {
		cmd.Stdin = bytes.NewReader(stdinPayload)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open Claude CLI stdout: %w", err)
	}
	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = defaultMaxOutputBytes
	stderr.limit = defaultMaxOutputBytes
	cmd.Stderr = &stderr

	timing := newTurnTiming("cli_spawn", false)
	timing.spawn = time.Now()
	defer timing.log(request.SessionID)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Claude CLI: %w", err)
	}
	defer guardProcessGroup(cmd).release()
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
		timing.observe(event)
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

	finalContent := processor.finalContent()
	if finalContent == "" {
		finalContent = strings.TrimSpace(processor.content.String())
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
		TimingMetadata:        timing.metadata(),
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
	return BindProviderSessionCursor(c.options.Identity, raw)
}

func (c *Client) buildArgs(request *llm.ChatRequest, model, prompt string, bridge mcpBridgeInvocation) []string {
	args := []string{"-p", "--output-format", "json", "--model", model}
	return withPositionalPrompt(c.appendCommonArgs(args, request, bridge), prompt)
}

func (c *Client) buildStreamArgs(request *llm.ChatRequest, model, prompt string, bridge mcpBridgeInvocation) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--model", model,
	}
	return withPositionalPrompt(c.appendCommonArgs(args, request, bridge), prompt)
}

// withPositionalPrompt appends the prompt after `--` so prompts that start with
// dashes (e.g. YAML front matter `---`) are not parsed as unknown CLI options.
func withPositionalPrompt(args []string, prompt string) []string {
	return append(args, "--", prompt)
}

func (c *Client) appendCommonArgs(args []string, request *llm.ChatRequest, bridge mcpBridgeInvocation) []string {
	if systemPrompt := buildSystemPrompt(request.SystemPrompt); systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	if c.options.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	} else if raw, ok := ResolveProviderSessionCursor(c.options.Identity, request.ProviderSessionCursor); ok && raw != "" {
		args = append(args, "--resume", raw)
	}
	// WHY: Claude CLI is itself the tool-running agent for Anthropic. A2gent
	// function schemas are not sent to the CLI, so expose Claude Code's native
	// tools explicitly and auto-allow only the capabilities that correspond to the
	// tools currently enabled in this A2gent request. This lets Sonnet edit files in
	// non-interactive mode without waiting for an invisible permission prompt.
	toolsArg, allowedArg, includeTools := claudeToolsArgs(request)
	if c.options.MCPOnly {
		// Parent tools must only execute through the scoped A2gent bridge.
		args = append(args, "--setting-sources", "", "--disable-slash-commands")
		toolsArg, allowedArg, includeTools = "", "", true
		if len(bridge.args) == 0 {
			args = append(args, "--strict-mcp-config")
		}
	}
	if includeTools {
		args = append(args, "--tools", toolsArg)
		if bridge.allowedTools != "" {
			if allowedArg == "" {
				allowedArg = bridge.allowedTools
			} else {
				allowedArg += "," + bridge.allowedTools
			}
		}
		if allowedArg != "" {
			args = append(args, "--allowedTools", allowedArg)
		}
	}
	if len(bridge.args) > 0 {
		args = append(args, bridge.args...)
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

func (c *Client) commandEnv() []string {
	return optionsCommandEnv(c.options)
}

// defaultMCPToolTimeoutMS sets Claude CLI's MCP tool-call timeout to 10 min:
// long enough for slow ComfyUI/Meshy generations, short enough that a stuck
// tool cannot block a session for long. Tools must return before it (Meshy
// hands back a resumable task_id after ~9 min); otherwise the CLI reports
// "The operation timed out." while the provider task keeps running.
const defaultMCPToolTimeoutMS = "600000"

// mcpTimeoutEnvKeys default to defaultMCPToolTimeoutMS. The CLI also aborts a
// call that sends no progress for 5 min (CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT);
// brute's bridge streams none, so delegations to a still-working child agent
// were cut at 300s. Matching it to the total timeout keeps one 10 min bound.
var mcpTimeoutEnvKeys = []string{"MCP_TOOL_TIMEOUT", "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT"}

func optionsCommandEnv(opts Options) []string {
	base := opts.Environment
	if len(base) == 0 {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+len(mcpTimeoutEnvKeys))
	env = append(env, base...)
	for _, key := range mcpTimeoutEnvKeys {
		if !envHasKey(base, key) {
			env = append(env, key+"="+defaultMCPToolTimeoutMS)
		}
	}
	return env
}

func envHasKey(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
