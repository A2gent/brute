package claudecli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const defaultPersistentIdle = 10 * time.Minute

// Each idle CLI process pins ~0.3 GB RSS (T-129 benchmark), so the pool is capped.
const defaultPersistentMaxProcesses = 4

// Clients are rebuilt between HTTP turns. The pool must outlive a Client, but
// never share a process across sessions, identities, environments or workdirs.
var persistentPool = struct {
	sync.Mutex
	processes map[string]*persistentProcess
	fallback  map[string]bool
}{processes: make(map[string]*persistentProcess), fallback: make(map[string]bool)}

type persistentLine struct {
	text string
	err  error
}
type persistentProcess struct {
	cmd                  *exec.Cmd
	stdin                io.WriteCloser
	lines                chan persistentLine
	stop, done           chan struct{}
	stopOnce, revokeOnce sync.Once
	revoke               func()
	fingerprint          [32]byte
	cursor               string
	busy                 bool
	idle                 *time.Timer
	lastUsed             time.Time
}

func persistentKey(opts Options, sessionID string) string {
	data, _ := json.Marshal([]string{opts.Identity, opts.Executable, opts.WorkDir, opts.ConfigDir, opts.HomePath, sessionID})
	return string(data)
}

func (c *Client) persistentEnabled(req *llm.ChatRequest) bool {
	// --max-budget-usd is process-cumulative: reusing it would change per-turn
	// budget semantics. Non-persisted sessions cannot safely establish continuity.
	return runtime.GOOS != "windows" && c.options.Persistent && !c.options.NoSessionPersistence && c.options.MaxBudgetUSD == "" && strings.TrimSpace(req.SessionID) != ""
}

func (c *Client) persistentMaxProcesses() int {
	if c.options.PersistentMaxProcesses > 0 {
		return c.options.PersistentMaxProcesses
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AAGENT_CLAUDE_CLI_PERSISTENT_MAX"))); err == nil && n > 0 {
		return n
	}
	return defaultPersistentMaxProcesses
}

// reservePersistentSlot evicts least recently used idle processes until a new
// one fits. Busy processes are never evicted; when all slots are busy the caller
// uses the spawn transport. Must be called with persistentPool locked.
func reservePersistentSlot(max int) bool {
	for len(persistentPool.processes) >= max {
		var oldestKey string
		var oldest *persistentProcess
		for key, p := range persistentPool.processes {
			if !p.busy && (oldest == nil || p.lastUsed.Before(oldest.lastUsed)) {
				oldestKey, oldest = key, p
			}
		}
		if oldest == nil {
			return false
		}
		delete(persistentPool.processes, oldestKey)
		if oldest.idle != nil {
			oldest.idle.Stop()
		}
		oldest.close()
	}
	return true
}

func (c *Client) persistentFingerprint(req *llm.ChatRequest, model string) ([32]byte, error) {
	copyReq := *req
	copyReq.ProviderSessionCursor = ""
	data, err := json.Marshal(struct {
		Args, Env        []string
		Tools            []llm.ToolDefinition
		Bridge           bool
		BridgeGeneration string
	}{withStdinPrompt(c.buildStreamArgs(&copyReq, model, "", mcpBridgeInvocation{})), c.commandEnv(), req.Tools, c.options.MCPBridge != nil, c.options.MCPBridgeGeneration})
	return sha256.Sum256(data), err
}

func (p *persistentProcess) close() {
	p.stopOnce.Do(func() {
		close(p.stop)
		p.revokeOnce.Do(p.revoke)
		// An idle child may already have exited and been reaped. Do not signal
		// its old process group after the OS has had time to recycle the PID.
		select {
		case <-p.done:
		default:
			killPersistentCommand(p.cmd)
		}
		_ = p.stdin.Close()
	})
	<-p.done
}

func closePersistentSession(opts Options, sessionID string) {
	key := persistentKey(opts, sessionID)
	persistentPool.Lock()
	p := persistentPool.processes[key]
	delete(persistentPool.processes, key)
	if p != nil && p.idle != nil {
		p.idle.Stop()
	}
	persistentPool.Unlock()
	if p != nil {
		p.close()
	}
}

// ClosePersistentProcesses stops idle and active CLI children during host shutdown.
func ClosePersistentProcesses() {
	persistentPool.Lock()
	processes := persistentPool.processes
	persistentPool.processes = make(map[string]*persistentProcess)
	for _, p := range processes {
		if p.idle != nil {
			p.idle.Stop()
		}
	}
	persistentPool.Unlock()
	for _, p := range processes {
		p.close()
	}
}

func (c *Client) startPersistent(ctx context.Context, req *llm.ChatRequest, model string, fingerprint [32]byte, timing *turnTiming) (*persistentProcess, error) {
	path, err := findExecutable(c.options.Executable)
	if err != nil {
		return nil, err
	}
	// The bridge belongs to the process, not the first request's deadline. Each
	// cancelled/failed active turn closes the process and revokes its token.
	bridgeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	bridgeCtx = context.WithValue(bridgeCtx, "session_id", req.SessionID)
	bridge := c.newMCPBridgeInvocation(bridgeCtx)
	revoke := func() { cancel(); bridge.revoke() }
	if bridge.err != nil {
		revoke()
		return nil, bridge.err
	}
	args := withStdinPrompt(c.buildStreamArgs(req, model, "", bridge))
	cmd := exec.Command(path, args...)
	cmd.Dir = c.options.WorkDir
	cmd.Env = c.commandEnv()
	configurePersistentCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		revoke()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		revoke()
		return nil, err
	}
	cmd.Stderr = &limitedBuffer{limit: defaultMaxOutputBytes}
	timing.spawn = time.Now()
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		revoke()
		return nil, err
	}
	p := &persistentProcess{cmd: cmd, stdin: stdin, lines: make(chan persistentLine, 32), stop: make(chan struct{}), done: make(chan struct{}), revoke: revoke, fingerprint: fingerprint, busy: true}
	go func() {
		defer close(p.done)
		defer p.revokeOnce.Do(p.revoke)
		defer close(p.lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), defaultMaxOutputBytes)
		for scanner.Scan() {
			select {
			case p.lines <- persistentLine{text: scanner.Text()}:
			case <-p.stop:
				_ = cmd.Wait()
				return
			}
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			killPersistentCommand(cmd)
		}
		waitErr := cmd.Wait()
		if scanErr == nil {
			scanErr = waitErr
		}
		if scanErr == nil {
			scanErr = io.EOF
		}
		select {
		case p.lines <- persistentLine{err: scanErr}:
		case <-p.stop:
		}
	}()
	return p, nil
}

func (c *Client) chatPersistent(ctx context.Context, req *llm.ChatRequest, model string, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error, bool) {
	if err := ctx.Err(); err != nil {
		return nil, err, true
	}
	key := persistentKey(c.options, req.SessionID)
	fingerprint, err := c.persistentFingerprint(req, model)
	if err != nil {
		return nil, err, true
	}
	payload, err := buildStreamInput(req, buildPrompt(req))
	if err != nil {
		return nil, err, true
	}
	timing := newTurnTiming("cli_persistent", false)
	defer timing.log(req.SessionID)
	persistentPool.Lock()
	if persistentPool.fallback[key] {
		persistentPool.Unlock()
		return nil, nil, false
	}
	p := persistentPool.processes[key]
	if p != nil && p.busy {
		persistentPool.Unlock()
		return nil, llm.UnsafeForRetry(fmt.Errorf("Claude CLI session already has an active turn")), true
	}
	if p != nil {
		dead := false
		select {
		case <-p.done:
			dead = true
		default:
		}
		// A missing/mismatched cursor means reset, fork or stale caller. Never append
		// that input to an unrelated in-memory conversation.
		if dead || p.fingerprint != fingerprint || p.cursor == "" || p.cursor != req.ProviderSessionCursor {
			delete(persistentPool.processes, key)
			if p.idle != nil {
				p.idle.Stop()
			}
			p.close()
			p = nil
		}
	}
	if p == nil && !reservePersistentSlot(c.persistentMaxProcesses()) {
		persistentPool.Unlock()
		return nil, nil, false
	}
	if p == nil {
		p, err = c.startPersistent(ctx, req, model, fingerprint, timing)
		if err != nil {
			persistentPool.Unlock()
			logging.Warn("Persistent Claude startup failed; using spawn transport: %v", err)
			return nil, nil, false
		}
		persistentPool.processes[key] = p
	} else {
		p.busy = true
		p.idle.Stop()
		timing.reused = true
	}
	persistentPool.Unlock()
	success := false
	defer func() {
		persistentPool.Lock()
		if success {
			p.busy = false
			p.lastUsed = time.Now()
			idle := c.options.PersistentIdleTimeout
			if idle <= 0 {
				idle = defaultPersistentIdle
			}
			p.idle = time.AfterFunc(idle, func() {
				persistentPool.Lock()
				if persistentPool.processes[key] != p || p.busy {
					persistentPool.Unlock()
					return
				}
				delete(persistentPool.processes, key)
				persistentPool.Unlock()
				p.close()
			})
		} else {
			if persistentPool.processes[key] == p {
				delete(persistentPool.processes, key)
			}
			// After a possibly side-effecting turn fails, only future turns use the
			// legacy transport. Replaying this turn would duplicate native/MCP tools.
			if ctx.Err() == nil {
				persistentPool.fallback[key] = true
				time.AfterFunc(defaultPersistentIdle, func() { persistentPool.Lock(); delete(persistentPool.fallback, key); persistentPool.Unlock() })
			}
		}
		persistentPool.Unlock()
		if !success {
			p.close()
		}
	}()
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() { p.close(); close(cancelDone) })
	var detachOnce sync.Once
	detach := func() {
		detachOnce.Do(func() {
			if !stopCancel() {
				<-cancelDone
			}
		})
	}
	defer detach()
	// A pipe write can block on a large image; cancellation closes the process
	// group and pipe, unblocking both writer and reader.
	if _, err = p.stdin.Write(payload); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err(), true
		}
		return nil, llm.UnsafeForRetry(fmt.Errorf("persistent Claude stdin: %w", err)), true
	}
	processor := newStreamProcessor(onEvent)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err(), true
		case line, ok := <-p.lines:
			if !ok || line.err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err(), true
				}
				return nil, llm.UnsafeForRetry(fmt.Errorf("persistent Claude stream ended before result: %v", line.err)), true
			}
			if strings.TrimSpace(line.text) == "" {
				continue
			}
			event, err := parseCLIStreamEnvelope(line.text)
			if err != nil {
				return nil, llm.UnsafeForRetry(err), true
			}
			timing.observe(event)
			if err = processor.handleEnvelope(event); err != nil {
				return nil, llm.UnsafeForRetry(err), true
			}
			if !processor.sawResult {
				continue
			}
			if processor.finalResult.IsError || strings.Contains(strings.ToLower(processor.finalResult.Subtype), "error") {
				return nil, llm.UnsafeForRetry(fmt.Errorf("%s", normalizeClaudeCLIErrorMessage(firstNonEmpty(processor.finalResult.Error, processor.finalResult.Result, "Claude CLI returned an error result")))), true
			}
			if err = processor.finalize(onEvent); err != nil {
				return nil, llm.UnsafeForRetry(err), true
			}
			detach()
			if ctx.Err() != nil {
				return nil, ctx.Err(), true
			}
			p.cursor = c.providerSessionCursor(firstNonEmpty(processor.providerSessionCursor, processor.finalResult.SessionID))
			success = true
			content := processor.finalContent()
			logging.LogResponseWithContent(processor.usage.InputTokens, processor.usage.OutputTokens, 0, content, nil)
			return &llm.ChatResponse{Content: content, Usage: processor.usage, StopReason: firstNonEmpty(processor.stopReason, processor.finalResult.StopReason, processor.finalResult.Subtype), ProviderSessionCursor: p.cursor, TimingMetadata: timing.metadata()}, nil, true
		}
	}
}
