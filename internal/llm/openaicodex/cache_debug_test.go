package openaicodex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

func debugBody(t *testing.T, tools, instructions string, items ...string) []byte {
	t.Helper()
	input := make([]json.RawMessage, len(items))
	for i, item := range items {
		input[i] = json.RawMessage(item)
	}
	body, err := json.Marshal(map[string]interface{}{"model": "test", "tools": json.RawMessage(tools), "instructions": instructions, "input": input})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCacheDebugTracksWirePrefixPerSession(t *testing.T) {
	t.Setenv("AAGENT_LLM_CACHE_DEBUG", "1")
	var tracker cacheDebugTracker
	first := debugBody(t, `[]`, "fixed", `{"type":"message","role":"user"}`)
	if got := tracker.begin("a", first); got.PrefixChange != "cold" || got.FirstDiff != -1 {
		t.Fatalf("first = %+v", got)
	}
	tracker.begin("b", debugBody(t, `[]`, "other"))
	if got := tracker.begin("a", first); got.PrefixChange != "none" || got.FirstDiff != -1 || got.Previous != 1 {
		t.Fatalf("repeat = %+v", got)
	}
	for _, tc := range []struct {
		name, tools, instructions, item, change string
		diff                                    int
	}{
		{"append", `[]`, "fixed", `{"type":"reasoning","encrypted_content":"opaque"}`, "input", 1},
		{"rewrite", `[]`, "fixed", `{"type":"reasoning","encrypted_content":"changed"}`, "input", 1},
		{"instructions", `[]`, "changed", `{"type":"reasoning","encrypted_content":"changed"}`, "instructions", -1},
		{"tools", `[{"name":"new"}]`, "changed", `{"type":"reasoning","encrypted_content":"changed"}`, "tools", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tracker.begin("a", debugBody(t, tc.tools, tc.instructions, `{"type":"message","role":"user"}`, tc.item))
			if got.PrefixChange != tc.change || got.FirstDiff != tc.diff {
				t.Fatalf("observation = %+v", got)
			}
		})
	}
}

func TestCacheDebugDisabledAndBounded(t *testing.T) {
	var tracker cacheDebugTracker
	t.Setenv("AAGENT_LLM_CACHE_DEBUG", "")
	if tracker.begin("a", []byte("not JSON")) != nil || tracker.sessions != nil {
		t.Fatal("disabled diagnostics did work")
	}
	t.Setenv("AAGENT_LLM_CACHE_DEBUG", "1")
	body := debugBody(t, `[]`, "fixed")
	for i := 0; i <= cacheDebugSessions; i++ {
		tracker.begin(fmt.Sprint(i), body)
	}
	if len(tracker.sessions) != cacheDebugSessions {
		t.Fatalf("sessions = %d", len(tracker.sessions))
	}
	if _, ok := tracker.sessions["0"]; ok {
		t.Fatal("oldest session not evicted")
	}
}

func TestCacheDebugLogsUsageAcrossClients(t *testing.T) {
	t.Setenv("AAGENT_LLM_CACHE_DEBUG", "1")
	if err := logging.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			const response = `{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":1024}}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, response)
				}
			}))
			defer server.Close()
			sessionID := "cache-debug-" + t.Name()
			for i := 0; i < 2; i++ {
				client := NewClientWithOptions("key", "model", server.URL, Options{PublicAPI: true})
				_, err := client.Chat(t.Context(), &llm.ChatRequest{SessionID: sessionID, SystemPrompt: "fixed", Messages: []llm.Message{{Role: "user", Content: "private fixture"}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			requests, usages := 0, 0
			for _, line := range logging.RecentLines(0) {
				if at := strings.Index(line, "LLM_CACHE_REQUEST "); at >= 0 {
					var observed cacheObservation
					if err := json.Unmarshal([]byte(line[at+len("LLM_CACHE_REQUEST "):]), &observed); err != nil {
						t.Fatal(err)
					}
					if observed.Session != sessionID {
						continue
					}
					requests++
					if strings.Contains(line, "private fixture") {
						t.Fatal("cache diagnostic logged prompt content")
					}
					if requests == 2 && (observed.Previous == 0 || observed.PrefixChange != "none") {
						t.Fatalf("new client lost previous request: %+v", observed)
					}
				}
				if strings.Contains(line, "LLM_CACHE_USAGE session="+fmt.Sprintf("%q", sessionID)) && strings.Contains(line, "input=2048 cached=1024 error=false") {
					usages++
				}
			}
			if requests != 2 || usages != 2 {
				t.Fatalf("logged requests=%d usage=%d", requests, usages)
			}
		})
	}
}
