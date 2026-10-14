package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

// T-129 warmup probe: checks that a pre-spawned idle persistent process makes no
// model requests. Opt-in, see persistent_bench_live_test.go.

// countingProxy forwards to the Anthropic API and counts model requests so a
// pre-spawned idle process can be checked for silent quota use.
type countingProxy struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []string
}

func newCountingProxy(t *testing.T) *countingProxy {
	upstream, _ := url.Parse("https://api.anthropic.com")
	p := &countingProxy{}
	rp := httputil.NewSingleHostReverseProxy(upstream)
	base := rp.Director
	rp.Director = func(r *http.Request) { base(r); r.Host = upstream.Host }
	rp.FlushInterval = -1
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		rp.ServeHTTP(rec, r)
		p.mu.Lock()
		p.hits = append(p.hits, fmt.Sprintf("%s %s %d", r.Method, r.URL.Path, rec.status))
		p.mu.Unlock()
	}))
	t.Cleanup(p.srv.Close)
	return p
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (p *countingProxy) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.hits...)
}

func modelRequests(hits []string) int {
	n := 0
	for _, h := range hits {
		if strings.Contains(h, "/v1/messages") && !strings.Contains(h, "count_tokens") {
			n++
		}
	}
	return n
}

// Warmup safety: a process started without input (what focus/typing pre-spawn
// would do) must not hit the model or leak; per-turn model requests must match
// between transports. The proxy distorts latency, so timings come from a
// separate direct pass and the proxy is used only for counting.
func TestLivePersistentBenchWarmupProbe(t *testing.T) {
	e := newBenchEnv(t)
	proxy := newCountingProxy(t)
	env := append(os.Environ(), "ANTHROPIC_BASE_URL="+proxy.srv.URL)
	out := map[string]any{"direct": prespawnProbe(t, e, nil, nil, 10*time.Second), "proxied": prespawnProbe(t, e, env, proxy, 20*time.Second)}

	for _, mode := range []string{"spawn", "persistent"} {
		req, opts, marker := e.newSession(t, mode, 0)
		opts.Environment = env
		var perTurn []int
		for turn := 1; turn <= 3; turn++ {
			before := len(proxy.snapshot())
			if _, _, err := e.turn(t, opts, req, marker, fmt.Sprintf("Turn %d. Reply with exactly OK. Do not use tools.", turn), t.Context()); err != nil {
				t.Fatalf("%s turn %d via proxy: %v", mode, turn, err)
			}
			perTurn = append(perTurn, modelRequests(proxy.snapshot()[before:]))
		}
		closePersistentSession(NewClientWithOptions("sonnet", opts).options, req.SessionID)
		out[mode+"_model_requests_per_turn"] = perTurn
	}
	out["all_proxy_paths"] = uniqueHits(proxy.snapshot())
	data, _ := json.MarshalIndent(out, "", " ")
	t.Logf("warmup probe %s", data)
	writeBenchOut(t, out)
	if n, _ := out["proxied"].(map[string]any)["idle_model_requests"].(int); n != 0 {
		t.Errorf("idle pre-spawned process made %d model requests", n)
	}
}

// prespawnProbe starts exactly the persistent command line, writes nothing for
// idle, then sends one turn and closes stdin.
func prespawnProbe(t *testing.T, e *benchEnv, env []string, proxy *countingProxy, idle time.Duration) map[string]any {
	out := map[string]any{}
	hits := func() []string {
		if proxy == nil {
			return nil
		}
		return proxy.snapshot()
	}
	req, opts, marker := e.newSession(t, "warm", len(env))
	opts.Environment = env
	client := NewClientWithOptions("sonnet", opts)
	bridge := client.newMCPBridgeInvocation(context.WithValue(context.Background(), "session_id", req.SessionID))
	defer bridge.revoke()
	path, _ := findExecutable(client.options.Executable)
	cmd := exec.Command(path, withStdinPrompt(client.buildStreamArgs(req, "sonnet", "", bridge))...)
	cmd.Dir, cmd.Env = client.options.WorkDir, client.commandEnv()
	configurePersistentCommand(cmd)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer killPersistentCommand(cmd)
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), defaultMaxOutputBytes)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var idleLines []string
	idleUntil := time.After(idle)
wait:
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				break wait
			}
			idleLines = append(idleLines, firstN(l, 120))
		case <-idleUntil:
			break wait
		}
	}
	idleTree := sampleTree(marker)
	e.mcp.mu.Lock()
	out["idle_mcp_methods"] = copyCounts(e.mcp.methods[req.SessionID])
	e.mcp.mu.Unlock()
	out["idle_stdout_lines"] = idleLines
	out["idle_proxy_hits"] = hits()
	out["idle_model_requests"] = modelRequests(hits())
	out["idle_rss_mb"] = idleTree.rssKB / 1024
	out["idle_procs"] = len(idleTree.pids)
	t.Logf("idle phase proxied=%t %v", proxy != nil, out)

	before := len(hits())
	req.Messages = []llm.Message{{Role: "user", Content: "Turn 1. Reply with exactly OK. Do not use tools."}}
	payload, _ := buildStreamInput(req, buildPrompt(req))
	sent := time.Now()
	_, _ = stdin.Write(payload)
	var firstLine, initAt, firstText, resultAt time.Duration
	deadline := time.After(3 * time.Minute)
read:
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				break read
			}
			if firstLine == 0 {
				firstLine = time.Since(sent)
			}
			// Field order is not stable ("result" lines may start with other keys).
			event, err := parseCLIStreamEnvelope(l)
			if err != nil {
				continue
			}
			if event.Type == "system" && event.Subtype == "init" && initAt == 0 {
				initAt = time.Since(sent)
			}
			if event.Type == "stream_event" && event.Event.Delta.Type == "text_delta" && firstText == 0 {
				firstText = time.Since(sent)
			}
			if event.Type == "result" {
				resultAt = time.Since(sent)
				break read
			}
		case <-deadline:
			t.Fatalf("pre-spawned turn timed out; proxy=%v stderr=%s", hits(), firstN(stderr.String(), 2000))
		}
	}
	out["turn_first_line_ms"] = firstLine.Milliseconds()
	out["turn_init_ms"] = initAt.Milliseconds()
	out["turn_ttft_ms"] = firstText.Milliseconds()
	out["turn_result_ms"] = resultAt.Milliseconds()
	out["turn_model_requests"] = modelRequests(hits()[before:])
	_ = stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	eof := time.Now()
	select {
	case err := <-exited:
		out["stdin_eof_exit_ms"] = time.Since(eof).Milliseconds()
		out["stdin_eof_exit_err"] = fmt.Sprint(err)
	case <-time.After(10 * time.Second):
		killPersistentCommand(cmd)
		<-exited
		out["stdin_eof_exit_err"] = "timeout; killed"
	}
	tree, _ := waitTreeGone(marker, 3*time.Second)
	out["leaked_pids"] = tree.pids
	out["stderr"] = firstN(stderr.String(), 400)
	return out
}

func copyCounts(in map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func uniqueHits(hits []string) map[string]int {
	out := map[string]int{}
	for _, h := range hits {
		out[h]++
	}
	return out
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
