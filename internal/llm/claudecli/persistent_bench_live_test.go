package claudecli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

// T-129 benchmark: spawn vs persistent Claude CLI on a long resumed JSONL with a
// real HTTP MCP server (per-process bearer tokens, like Brute's bridge). Opt-in
// because it spends real Claude quota:
//
//	AAGENT_CLAUDE_BENCH_LIVE=1 AAGENT_CLAUDE_BENCH_JSONL=/path/long.jsonl \
//	  [AAGENT_CLAUDE_BENCH_TRIALS=10] [AAGENT_CLAUDE_BENCH_OUT_DIR=/tmp/results] \
//	  go test -v ./internal/llm/claudecli -run '^TestLivePersistentBench' -count=1 -timeout 60m

// ---- MCP server mirroring internal/http/mcp_bridge.go token semantics ----

type benchToken struct {
	session  string
	ctx      context.Context
	cancel   context.CancelFunc
	revoked  bool
	requests int
}

type benchMCP struct {
	srv         *httptest.Server
	mu          sync.Mutex
	tokens      map[string]*benchToken
	minted      map[string]int // per Brute session
	revoked     map[string]int
	rejected    map[string]int // requests carrying unknown/revoked tokens
	methods     map[string]map[string]int
	calls       map[string]int // tool marker -> executions
	waitStarted chan string
	waitEnded   chan string
}

func newBenchMCP(t *testing.T) *benchMCP {
	m := &benchMCP{tokens: map[string]*benchToken{}, minted: map[string]int{}, revoked: map[string]int{}, rejected: map[string]int{},
		methods: map[string]map[string]int{}, calls: map[string]int{}, waitStarted: make(chan string, 8), waitEnded: make(chan string, 8)}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *benchMCP) hook(ctx context.Context, sessionID string) (string, func(), error) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)
	tctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.tokens[token] = &benchToken{session: sessionID, ctx: tctx, cancel: cancel}
	m.minted[sessionID]++
	m.mu.Unlock()
	var once sync.Once
	revoke := func() {
		once.Do(func() {
			cancel()
			m.mu.Lock()
			m.tokens[token].revoked = true
			m.revoked[sessionID]++
			m.mu.Unlock()
		})
	}
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"a2gent": map[string]any{
		"type": "http", "url": m.srv.URL + "/mcp/" + sessionID, "headers": map[string]string{"Authorization": "Bearer " + token}}}})
	return string(cfg), revoke, nil
}

func (m *benchMCP) active(session string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, tok := range m.tokens {
		if tok.session == session && !tok.revoked {
			n++
		}
	}
	return n
}

func (m *benchMCP) count(marker string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[marker]
}

func benchToolList() []map[string]any {
	schema := func(props ...string) map[string]any {
		p := map[string]any{}
		for _, name := range props {
			p[name] = map[string]any{"type": "string", "description": "The " + name + " parameter for this operation."}
		}
		return map[string]any{"type": "object", "properties": p, "required": props[:1]}
	}
	tools := []map[string]any{
		{"name": "bench_marker", "description": "Record a benchmark marker. Call only when explicitly asked.", "inputSchema": schema("marker")},
		{"name": "bench_wait", "description": "Wait for an external benchmark event. Call only when explicitly asked.", "inputSchema": schema("marker")},
	}
	// Brute's bridge exposes ~40 integration tools; filler keeps tools/list and
	// the tool-definition prompt size in a realistic range.
	for i := 0; i < 38; i++ {
		tools = append(tools, map[string]any{"name": fmt.Sprintf("integration_%02d", i),
			"description": strings.Repeat(fmt.Sprintf("Integration operation %d queries an external service and returns structured results. ", i), 4),
			"inputSchema": schema("operation", "query", "project", "max_results")})
	}
	return tools
}

func (m *benchMCP) handle(w http.ResponseWriter, r *http.Request) {
	session := strings.TrimPrefix(r.URL.Path, "/mcp/")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	m.mu.Lock()
	tok := m.tokens[token]
	if tok == nil || tok.revoked || tok.session != session {
		m.rejected[session]++
		m.mu.Unlock()
		http.Error(w, "invalid or revoked MCP bridge token", http.StatusUnauthorized)
		return
	}
	tok.requests++
	m.mu.Unlock()
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name            string            `json:"name"`
			Arguments       map[string]string `json:"arguments"`
			ProtocolVersion string            `json:"protocolVersion"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	if m.methods[session] == nil {
		m.methods[session] = map[string]int{}
	}
	m.methods[session][msg.Method]++
	m.mu.Unlock()
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch msg.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": firstNonEmpty(msg.Params.ProtocolVersion, "2024-11-05"),
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "a2gent-bench", "version": "1"}}
	case "tools/list":
		result = map[string]any{"tools": benchToolList()}
	case "tools/call":
		marker := msg.Params.Arguments["marker"]
		m.mu.Lock()
		m.calls[marker]++
		m.mu.Unlock()
		text := "recorded " + marker
		if msg.Params.Name == "bench_wait" {
			m.waitStarted <- marker
			// Mirrors a held bridge call: only token revocation (or a long timeout) ends it.
			select {
			case <-tok.ctx.Done():
				text = "cancelled"
			case <-time.After(2 * time.Minute):
			}
			m.waitEnded <- marker
		}
		result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	default:
		result = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
}

// ---- process-tree sampling ----

type benchTree struct {
	pids  []int
	rssKB int64
}

// sampleTree finds claude processes whose argv carries marker (it is embedded in
// the system prompt) plus all descendants.
func sampleTree(marker string) benchTree {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=,command=").Output()
	if err != nil {
		return benchTree{}
	}
	children := map[int][]int{}
	rss := map[int]int64{}
	var roots []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		kb, _ := strconv.ParseInt(f[2], 10, 64)
		rss[pid] = kb
		children[ppid] = append(children[ppid], pid)
		if pid != os.Getpid() && f[3] != "ps" && strings.Contains(line, marker) {
			roots = append(roots, pid)
		}
	}
	var tree benchTree
	seen := map[int]bool{}
	for len(roots) > 0 {
		pid := roots[0]
		roots = roots[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		tree.pids = append(tree.pids, pid)
		tree.rssKB += rss[pid]
		roots = append(roots, children[pid]...)
	}
	return tree
}

func waitTreeGone(marker string, timeout time.Duration) (benchTree, time.Duration) {
	start := time.Now()
	for {
		tree := sampleTree(marker)
		if len(tree.pids) == 0 || time.Since(start) > timeout {
			return tree, time.Since(start)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// peakSampler polls RSS while a turn runs.
func peakSampler(marker string) func() (peakKB int64, peakProcs int) {
	stop := make(chan struct{})
	done := make(chan struct{})
	var peak int64
	var procs int
	go func() {
		defer close(done)
		for {
			tree := sampleTree(marker)
			if tree.rssKB > peak {
				peak = tree.rssKB
			}
			if len(tree.pids) > procs {
				procs = len(tree.pids)
			}
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	return func() (int64, int) { close(stop); <-done; return peak, procs }
}

// ---- long resume JSONL fixture ----

type benchEnv struct {
	mcp        *benchMCP
	workDir    string
	projectDir string
	source     []byte
	sourceID   string
}

func newBenchEnv(t *testing.T) *benchEnv {
	if os.Getenv("AAGENT_CLAUDE_BENCH_LIVE") != "1" {
		t.Skip("set AAGENT_CLAUDE_BENCH_LIVE=1 and AAGENT_CLAUDE_BENCH_JSONL to run")
	}
	t.Setenv("AAGENT_CLAUDE_CLI_PERSISTENT", "false")
	source, err := os.ReadFile(os.Getenv("AAGENT_CLAUDE_BENCH_JSONL"))
	if err != nil {
		t.Fatal(err)
	}
	sourceID := regexp.MustCompile(`"sessionId":"([0-9a-f-]{36})"`).FindSubmatch(source)
	if sourceID == nil {
		t.Fatal("JSONL has no sessionId")
	}
	dir, err := os.MkdirTemp("", "t129-bench-")
	if err != nil {
		t.Fatal(err)
	}
	// Claude names project dirs after the real cwd (/private/var/... on macOS).
	real, _ := filepath.EvalSymlinks(dir)
	home, _ := os.UserHomeDir()
	projectDir := filepath.Join(home, ".claude", "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(real, "-"))
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ClosePersistentProcesses()
		_ = os.RemoveAll(dir)
		if strings.Contains(projectDir, "t129-bench-") {
			_ = os.RemoveAll(projectDir)
		}
	})
	return &benchEnv{mcp: newBenchMCP(t), workDir: real, projectDir: projectDir, source: source, sourceID: string(sourceID[1])}
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// newSession copies the long JSONL under a fresh Claude session id so each arm
// resumes identical history and never shares provider state with another arm.
func (e *benchEnv) newSession(t *testing.T, mode string, trial int) (*llm.ChatRequest, Options, string) {
	id := newUUID()
	data := bytes.ReplaceAll(e.source, []byte(e.sourceID), []byte(id))
	if err := os.WriteFile(filepath.Join(e.projectDir, id+".jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprintf("t129%s%d%s", mode, trial, strings.ReplaceAll(id[:8], "-", ""))
	req := &llm.ChatRequest{SessionID: marker,
		SystemPrompt:          "Benchmark run " + marker + ". This is a latency benchmark; ignore any earlier unfinished work in this conversation. Follow the latest user message literally.",
		Tools:                 []llm.ToolDefinition{{Name: "read"}, {Name: "grep"}, {Name: "glob"}},
		ProviderSessionCursor: BindProviderSessionCursor("bench", id)}
	opts := Options{WorkDir: e.workDir, Identity: "bench", Persistent: mode == "persistent", PersistentIdleTimeout: 5 * time.Minute,
		MCPBridge: e.mcp.hook, MCPBridgeGeneration: "bench"}
	return req, opts, marker
}

type benchTurn struct {
	Mode, Marker    string
	Trial, Turn     int
	OrderFirst      bool
	Timing          map[string]any
	PeakRSSKB       int64
	PeakProcs       int
	IdleRSSKB       int64
	IdleProcs       int
	InputTokens     int
	CacheReadTokens int
	Err             string
}

func (e *benchEnv) turn(t *testing.T, opts Options, req *llm.ChatRequest, marker, prompt string, ctx context.Context) (benchTurn, *llm.ChatResponse, error) {
	req.Messages = []llm.Message{{Role: "user", Content: prompt}}
	// The agent loop sets session_id on the request context; spawn-mode bridge minting needs it.
	ctx = context.WithValue(ctx, "session_id", req.SessionID)
	stop := peakSampler(marker)
	resp, err := NewClientWithOptions("sonnet", opts).ChatStream(ctx, req, nil)
	peak, procs := stop()
	time.Sleep(300 * time.Millisecond)
	idle := sampleTree(marker)
	rec := benchTurn{Marker: marker, PeakRSSKB: peak, PeakProcs: procs, IdleRSSKB: idle.rssKB, IdleProcs: len(idle.pids)}
	if err != nil {
		rec.Err = err.Error()
		return rec, nil, err
	}
	rec.Timing = resp.TimingMetadata
	rec.InputTokens = resp.Usage.InputTokens
	rec.CacheReadTokens = resp.Usage.CachedInputTokens
	req.ProviderSessionCursor = resp.ProviderSessionCursor
	return rec, resp, nil
}

type benchArm struct {
	Mode, Marker                         string
	Trial                                int
	Turns                                []benchTurn
	Minted, Revoked, Rejected, ActiveEnd int
	Methods                              map[string]int
	ToolExecutions                       int
	LeakedPIDs                           []int
	ExitWait                             string
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	idx := int(p*float64(len(s))+0.999999) - 1 // nearest-rank
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}

func metric(turn benchTurn, key string) (float64, bool) {
	v, ok := turn.Timing[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func TestLivePersistentBenchSeries(t *testing.T) {
	e := newBenchEnv(t)
	trials, _ := strconv.Atoi(os.Getenv("AAGENT_CLAUDE_BENCH_TRIALS"))
	if trials <= 0 {
		trials = 10
	}
	prompts := []string{
		"Turn 1. Reply with exactly OK. Do not use tools.",
		"Turn 2. Reply with exactly OK. Do not use tools.",
		"Turn 3. Call the mcp__a2gent__bench_marker tool exactly once with marker \"%s\", then reply with exactly OK.",
		"Turn 4. Reply with exactly OK. Do not use tools.",
		"Turn 5. Reply with exactly OK. Do not use tools.",
	}
	var arms []benchArm
	for trial := 0; trial < trials; trial++ {
		order := []string{"spawn", "persistent"}
		if trial%2 == 1 {
			order = []string{"persistent", "spawn"}
		}
		for i, mode := range order {
			req, opts, marker := e.newSession(t, mode, trial)
			arm := benchArm{Mode: mode, Marker: marker, Trial: trial}
			for turnNo, prompt := range prompts {
				if strings.Contains(prompt, "%s") {
					prompt = fmt.Sprintf(prompt, marker+"-t3")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
				rec, _, err := e.turn(t, opts, req, marker, prompt, ctx)
				cancel()
				rec.Mode, rec.Trial, rec.Turn, rec.OrderFirst = mode, trial, turnNo+1, i == 0
				arm.Turns = append(arm.Turns, rec)
				if err != nil {
					t.Errorf("%s trial %d turn %d: %v", mode, trial, turnNo+1, err)
					break
				}
				if mode == "persistent" && turnNo > 0 && rec.Timing["llm_process_reused"] != true {
					t.Errorf("%s trial %d turn %d not reused: %v", mode, trial, turnNo+1, rec.Timing)
				}
			}
			closePersistentSession(NewClientWithOptions("sonnet", opts).options, req.SessionID)
			tree, waited := waitTreeGone(marker, 5*time.Second)
			arm.LeakedPIDs, arm.ExitWait = tree.pids, waited.String()
			e.mcp.mu.Lock()
			arm.Minted, arm.Revoked, arm.Rejected, arm.Methods = e.mcp.minted[marker], e.mcp.revoked[marker], e.mcp.rejected[marker], e.mcp.methods[marker]
			e.mcp.mu.Unlock()
			arm.ActiveEnd = e.mcp.active(marker)
			arm.ToolExecutions = e.mcp.count(marker + "-t3")
			if len(arm.LeakedPIDs) > 0 || arm.ActiveEnd != 0 || arm.ToolExecutions != 1 {
				t.Errorf("%s trial %d: leaked=%v activeTokens=%d toolExecutions=%d", mode, trial, arm.LeakedPIDs, arm.ActiveEnd, arm.ToolExecutions)
			}
			t.Logf("arm %s trial=%d minted=%d revoked=%d rejected=%d methods=%v tool=%d exitWait=%s", mode, trial, arm.Minted, arm.Revoked, arm.Rejected, arm.Methods, arm.ToolExecutions, arm.ExitWait)
			arms = append(arms, arm)
		}
	}
	summarizeBench(t, arms)
}

func summarizeBench(t *testing.T, arms []benchArm) {
	type key struct {
		mode string
		turn int
	}
	values := map[key]map[string][]float64{}
	paired := map[int]map[int]map[string]float64{} // turn -> trial -> mode -> ttft
	for _, arm := range arms {
		for _, turn := range arm.Turns {
			if turn.Err != "" {
				continue
			}
			k := key{arm.Mode, turn.Turn}
			if values[k] == nil {
				values[k] = map[string][]float64{}
			}
			for _, name := range []string{"llm_ttft_ms", "llm_first_stream_line_ms", "llm_init_to_text_ms", "llm_turn_result_ms"} {
				if v, ok := metric(turn, name); ok {
					values[k][name] = append(values[k][name], v)
				}
			}
			values[k]["peak_rss_mb"] = append(values[k]["peak_rss_mb"], float64(turn.PeakRSSKB)/1024)
			values[k]["idle_rss_mb"] = append(values[k]["idle_rss_mb"], float64(turn.IdleRSSKB)/1024)
			if v, ok := metric(turn, "llm_ttft_ms"); ok {
				if paired[turn.Turn] == nil {
					paired[turn.Turn] = map[int]map[string]float64{}
				}
				if paired[turn.Turn][arm.Trial] == nil {
					paired[turn.Turn][arm.Trial] = map[string]float64{}
				}
				paired[turn.Turn][arm.Trial][arm.Mode] = v
			}
		}
	}
	for turnNo := 1; turnNo <= 5; turnNo++ {
		for _, mode := range []string{"spawn", "persistent"} {
			v := values[key{mode, turnNo}]
			line := fmt.Sprintf("turn=%d %-10s n=%d", turnNo, mode, len(v["llm_ttft_ms"]))
			for _, name := range []string{"llm_ttft_ms", "llm_first_stream_line_ms", "llm_init_to_text_ms", "llm_turn_result_ms", "peak_rss_mb", "idle_rss_mb"} {
				line += fmt.Sprintf(" %s p50=%.0f p95=%.0f", strings.TrimPrefix(name, "llm_"), percentile(v[name], .5), percentile(v[name], .95))
			}
			t.Log(line)
		}
		var diffs []float64
		for _, byMode := range paired[turnNo] {
			if s, ok := byMode["spawn"]; ok {
				if p, ok := byMode["persistent"]; ok {
					diffs = append(diffs, s-p)
				}
			}
		}
		t.Logf("turn=%d paired spawn-persistent ttft n=%d median=%.0f min=%.0f max=%.0f", turnNo, len(diffs), percentile(diffs, .5), percentile(diffs, 0), percentile(diffs, 1))
	}
	writeBenchOut(t, arms)
}

func writeBenchOut(t *testing.T, v any) {
	if dir := os.Getenv("AAGENT_CLAUDE_BENCH_OUT_DIR"); dir != "" {
		data, _ := json.MarshalIndent(v, "", " ")
		if err := os.WriteFile(filepath.Join(dir, t.Name()+".json"), data, 0600); err != nil {
			t.Error(err)
		}
	}
}

// Cancellation during a held MCP call and a hard crash mid-tool. Both must end
// the whole process tree, revoke the token and never execute the tool again.
func TestLivePersistentBenchCancellation(t *testing.T) {
	e := newBenchEnv(t)
	type result struct {
		Mode, Scenario                                   string
		CancelToReturn, TreeGoneAfter                    string
		WaitEndedByRevoke, ErrUnsafe                     bool
		Err                                              string
		ExecutionsAfterCancel, ExecutionsAfterNextTurn   int
		NextTransport                                    any
		NextReused                                       any
		ActiveTokensAfterClose, LeakedPIDs, RejectedReqs int
	}
	var results []result
	for _, sc := range []struct{ mode, scenario string }{{"spawn", "cancel"}, {"persistent", "cancel"}, {"persistent", "crash"}} {
		req, opts, marker := e.newSession(t, sc.mode, 99)
		res := result{Mode: sc.mode, Scenario: sc.scenario}
		if _, _, err := e.turn(t, opts, req, marker, "Turn 1. Reply with exactly OK. Do not use tools.", t.Context()); err != nil {
			t.Fatal(err)
		}
		cursorAfterTurn1 := req.ProviderSessionCursor
		waitMarker := marker + "-wait"
		ctx, cancel := context.WithCancel(t.Context())
		var cancelledAt time.Time
		go func() {
			select {
			case <-e.mcp.waitStarted:
			case <-time.After(3 * time.Minute):
			}
			cancelledAt = time.Now()
			if sc.scenario == "cancel" {
				cancel()
				return
			}
			// Simulates an abrupt CLI crash while a side-effecting tool is in flight.
			if tree := sampleTree(marker); len(tree.pids) > 0 {
				_ = syscall.Kill(tree.pids[0], syscall.SIGKILL)
			}
		}()
		_, _, err := e.turn(t, opts, req, marker, fmt.Sprintf("Turn 2. Call the mcp__a2gent__bench_wait tool exactly once with marker \"%s\", then reply with exactly DONE.", waitMarker), ctx)
		res.CancelToReturn = time.Since(cancelledAt).String()
		if err != nil {
			res.Err = err.Error()
		}
		res.ErrUnsafe = llm.IsUnsafeForRetry(err)
		tree, waited := waitTreeGone(marker, 5*time.Second)
		res.TreeGoneAfter = waited.String()
		select {
		case <-e.mcp.waitEnded:
			res.WaitEndedByRevoke = true
		case <-time.After(5 * time.Second):
		}
		cancel()
		res.ExecutionsAfterCancel = e.mcp.count(waitMarker)
		if len(tree.pids) > 0 {
			t.Errorf("%s/%s: tree still alive %v", sc.mode, sc.scenario, tree.pids)
		}
		req.ProviderSessionCursor = cursorAfterTurn1
		rec, _, err := e.turn(t, opts, req, marker, "Turn 3. Reply with exactly OK. Do not call any tools.", t.Context())
		if err != nil {
			t.Errorf("%s/%s next turn: %v", sc.mode, sc.scenario, err)
		}
		res.NextTransport, res.NextReused = rec.Timing["llm_transport"], rec.Timing["llm_process_reused"]
		res.ExecutionsAfterNextTurn = e.mcp.count(waitMarker)
		closePersistentSession(NewClientWithOptions("sonnet", opts).options, req.SessionID)
		persistentPool.Lock()
		delete(persistentPool.fallback, persistentKey(NewClientWithOptions("sonnet", opts).options, req.SessionID))
		persistentPool.Unlock()
		tree, _ = waitTreeGone(marker, 5*time.Second)
		res.LeakedPIDs = len(tree.pids)
		res.ActiveTokensAfterClose = e.mcp.active(marker)
		e.mcp.mu.Lock()
		res.RejectedReqs = e.mcp.rejected[marker]
		e.mcp.mu.Unlock()
		if res.ExecutionsAfterCancel != 1 || res.ExecutionsAfterNextTurn != 1 || res.ActiveTokensAfterClose != 0 || res.LeakedPIDs != 0 {
			t.Errorf("%s/%s: %+v", sc.mode, sc.scenario, res)
		}
		data, _ := json.Marshal(res)
		t.Logf("cancellation %s", data)
		results = append(results, res)
	}
	writeBenchOut(t, results)
}
