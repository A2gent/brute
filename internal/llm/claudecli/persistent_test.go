package claudecli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

func persistentFixture(t *testing.T) (Options, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
printf 'spawn\n' >> "$TRACE"
for arg in "$@"; do printf '%s\n' "$arg" >> "$TRACE"; done
printf '{"type":"system","subtype":"init","session_id":"cursor"}\n'
while IFS= read -r line; do
 printf '%s\n' "$line" >> "$INPUTS"
 case "$line" in *hang*) sleep 30;; *malformed*) printf 'invalid\n'; continue;; esac
 printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}}\n'
 printf '{"type":"result","subtype":"success","result":"ok","session_id":"cursor"}\n'
done
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return Options{Executable: path, WorkDir: dir, Identity: "identity", Persistent: true, PersistentIdleTimeout: time.Minute,
		Environment: append(os.Environ(), "TRACE="+filepath.Join(dir, "trace"), "INPUTS="+filepath.Join(dir, "inputs"))}, dir
}

func TestPersistentReusesAcrossClientsAndKeepsBridge(t *testing.T) {
	opts, dir := persistentFixture(t)
	var minted, revoked atomic.Int32
	var bridgeContext context.Context
	opts.MCPBridge = func(ctx context.Context, sessionID string) (string, func(), error) {
		bridgeContext = ctx
		minted.Add(1)
		return `{"mcpServers":{}}`, func() { revoked.Add(1) }, nil
	}
	req := &llm.ChatRequest{SessionID: dir, SystemPrompt: "stable", Messages: []llm.Message{{Role: "user", Content: "hello"}}, Tools: []llm.ToolDefinition{{Name: "read"}}}
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(t.Context())
		resp, err := NewClientWithOptions("sonnet", opts).ChatStream(ctx, req, nil)
		cancel() // Ending a completed turn must not end the process or MCP token.
		if err != nil {
			t.Fatal(err)
		}
		req.ProviderSessionCursor = resp.ProviderSessionCursor
		if resp.ProviderSessionCursor != "identity|cursor" {
			t.Fatal(resp)
		}
		if resp.TimingMetadata["llm_process_reused"] != (i > 0) {
			t.Fatal(resp.TimingMetadata)
		}
		if _, ok := resp.TimingMetadata["llm_first_text_delta_at"]; !ok {
			t.Fatal(resp.TimingMetadata)
		}
	}
	trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
	if strings.Count(string(trace), "spawn\n") != 1 {
		t.Fatal(string(trace))
	}
	if minted.Load() != 1 || revoked.Load() != 0 {
		t.Fatalf("bridge minted=%d revoked=%d", minted.Load(), revoked.Load())
	}
	if bridgeContext.Err() != nil {
		t.Fatal("completed request cancelled bridge")
	}
	// Configuration changes replace the process, preserving resume and permissions.
	req.SystemPrompt = "changed"
	resp, err := NewClientWithOptions("sonnet", opts).ChatStream(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.TimingMetadata["llm_process_reused"] != false {
		t.Fatal(resp.TimingMetadata)
	}
	if minted.Load() != 2 || revoked.Load() != 1 {
		t.Fatal("bridge lifetime")
	}
	trace, _ = os.ReadFile(filepath.Join(dir, "trace"))
	for _, want := range []string{"--resume\ncursor", "--permission-mode\nacceptEdits", "--allowedTools\nGlob,Grep,LS,Read,mcp__a2gent__*"} {
		if !strings.Contains(string(trace), want) {
			t.Fatalf("missing %s in %s", want, trace)
		}
	}
	closePersistentSession(opts, dir)
	if revoked.Load() != 2 {
		t.Fatal("bridge not revoked on close")
	}
}

func TestPersistentCancellationAndMalformedDoNotReplay(t *testing.T) {
	for _, text := range []string{"hang", "malformed"} {
		t.Run(text, func(t *testing.T) {
			opts, dir := persistentFixture(t)
			var revoked atomic.Int32
			opts.MCPBridge = func(context.Context, string) (string, func(), error) { return `{}`, func() { revoked.Add(1) }, nil }
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			_, err := NewClientWithOptions("sonnet", opts).ChatStream(ctx, &llm.ChatRequest{SessionID: dir, Messages: []llm.Message{{Role: "user", Content: text}}}, nil)
			if revoked.Load() != 1 {
				t.Fatal("cancel/error did not revoke bridge")
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if text == "hang" && ctx.Err() == nil {
				t.Fatal(err)
			}
			if text == "malformed" && !llm.IsUnsafeForRetry(err) {
				t.Fatal(err)
			}
			trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
			if strings.Count(string(trace), "spawn\n") != 1 {
				t.Fatal("unsafe replay", string(trace))
			}
		})
	}
}

func TestPersistentIdleExpiresAndImagesUseStdin(t *testing.T) {
	opts, dir := persistentFixture(t)
	opts.PersistentIdleTimeout = 30 * time.Millisecond
	var revoked atomic.Int32
	opts.MCPBridge = func(context.Context, string) (string, func(), error) {
		return `{"mcpServers":{}}`, func() { revoked.Add(1) }, nil
	}
	req := &llm.ChatRequest{SessionID: dir, Messages: []llm.Message{{Role: "user", Content: "image", Images: []llm.Image{{MediaType: "image/png", DataBase64: "aGVsbG8="}}}}}
	resp, err := NewClientWithOptions("sonnet", opts).ChatStream(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.ProviderSessionCursor = resp.ProviderSessionCursor
	deadline := time.Now().Add(time.Second)
	for revoked.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if revoked.Load() != 1 {
		t.Fatal("idle bridge was not revoked")
	}
	resp, err = NewClientWithOptions("sonnet", opts).ChatStream(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.TimingMetadata["llm_process_reused"] != false {
		t.Fatal("reused expired process")
	}
	inputs, _ := os.ReadFile(filepath.Join(dir, "inputs"))
	if !strings.Contains(string(inputs), `"type":"image"`) || !strings.Contains(string(inputs), "aGVsbG8=") {
		t.Fatal(string(inputs))
	}
	closePersistentSession(opts, dir)
}

func TestSpawnStreamTiming(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeClaudeStreamLines(t, dir, filepath.Join(dir, "args"),
		`{"type":"system","subtype":"init","session_id":"cursor"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}}`,
		`{"type":"result","subtype":"success","result":"ok"}`)
	t.Setenv("ARGS_FILE", filepath.Join(dir, "args"))
	resp, err := NewClientWithOptions("sonnet", Options{Executable: fake, WorkDir: dir}).ChatStream(t.Context(), &llm.ChatRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var previous time.Time
	for _, key := range []string{"llm_process_spawn_at", "llm_first_stream_line_at", "llm_system_init_at", "llm_first_text_delta_at", "llm_result_at"} {
		raw, ok := resp.TimingMetadata[key].(string)
		if !ok {
			t.Fatalf("missing %s: %v", key, resp.TimingMetadata)
		}
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || at.Before(previous) {
			t.Fatalf("invalid timestamp %s", raw)
		}
		previous = at
	}
}

func TestPersistentInvalidatesConfigurationAndCursor(t *testing.T) {
	for _, change := range []string{"tools", "permissions", "environment", "model", "bridge", "cursor"} {
		t.Run(change, func(t *testing.T) {
			opts, dir := persistentFixture(t)
			req := &llm.ChatRequest{SessionID: dir, Messages: []llm.Message{{Role: "user", Content: "hello"}}}
			client := NewClientWithOptions("sonnet", opts)
			t.Cleanup(func() { closePersistentSession(client.options, dir) })
			resp, err := client.ChatStream(t.Context(), req, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.ProviderSessionCursor = resp.ProviderSessionCursor
			switch change {
			case "tools":
				req.Tools = []llm.ToolDefinition{{Name: "write"}}
			case "permissions":
				opts.PermissionMode = "plan"
			case "environment":
				opts.Environment = append(opts.Environment, "TEST_VERSION=2")
			case "model":
				req.Model = "haiku"
			case "bridge":
				opts.MCPBridgeGeneration = "new-generation"
			case "cursor":
				req.ProviderSessionCursor = "identity|other-cursor"
			}
			resp, err = NewClientWithOptions("sonnet", opts).ChatStream(t.Context(), req, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.TimingMetadata["llm_process_reused"] != false {
				t.Fatalf("reused after changing %s", change)
			}
		})
	}
}

func TestPersistentBridgeFailureFallsBackBeforeSending(t *testing.T) {
	opts, dir := persistentFixture(t)
	// The spawn path needs no stdin. If the failed bridge starts a persistent
	// process anyway, it would consume the input and incorrectly succeed.
	script := `#!/bin/sh
printf 'spawn\n' >> "$TRACE"
printf '{"type":"result","subtype":"success","result":"fallback"}\n'
`
	if err := os.WriteFile(opts.Executable, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	opts.MCPBridge = func(context.Context, string) (string, func(), error) {
		return "", nil, fmt.Errorf("bridge unavailable")
	}
	resp, err := NewClientWithOptions("sonnet", opts).ChatStream(t.Context(), &llm.ChatRequest{SessionID: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.TimingMetadata["llm_transport"] != "cli_spawn" {
		t.Fatal(resp.TimingMetadata)
	}
	trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
	if strings.Count(string(trace), "spawn\n") != 1 {
		t.Fatal(string(trace))
	}
}

func TestPersistentCallbackFailureEvictsProcess(t *testing.T) {
	opts, dir := persistentFixture(t)
	sentinel := errors.New("callback stopped")
	_, err := NewClientWithOptions("sonnet", opts).ChatStream(t.Context(), &llm.ChatRequest{SessionID: dir, Messages: []llm.Message{{Role: "user", Content: "hello"}}}, func(llm.StreamEvent) error { return sentinel })
	if !errors.Is(err, sentinel) || !llm.IsUnsafeForRetry(err) {
		t.Fatal(err)
	}
	persistentPool.Lock()
	p := persistentPool.processes[persistentKey(NewClientWithOptions("sonnet", opts).options, dir)]
	persistentPool.Unlock()
	if p != nil {
		t.Fatal("process remained cached")
	}
}
