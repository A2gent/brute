package lmstudio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestPrepareRequestLifecycle(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"success", "hook-error", "http-error", "cancel", "nil-cleanup", "default"} {
			t.Run(fmtTestName(stream, mode), func(t *testing.T) {
				calls, cleaned, sent := 0, 0, 0
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				request := &llm.ChatRequest{SessionID: "private-session", ProviderSessionCursor: "private-cursor", Messages: []llm.Message{{Role: "user", Content: "hi"}}}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sent++
					body, _ := io.ReadAll(r.Body)
					if strings.Contains(string(body), "private-") || strings.Contains(string(body), "relay-secret") {
						t.Error("metadata leaked into body")
					}
					if mode == "default" {
						if r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("X-A2gent-Session") != "" {
							t.Error("default headers changed")
						}
					} else if r.Header.Get("Authorization") != "Bearer relay-secret" || r.Header.Get("X-A2gent-Session") != "private-session" || len(r.Header.Values("X-Multi")) != 2 {
						t.Errorf("hook headers missing: %v", r.Header)
					}
					if mode == "http-error" {
						w.WriteHeader(500)
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
					}
				}))
				defer server.Close()
				c := NewClient("provider-key", "test", server.URL)
				hookErr := errors.New("hook failed")
				if mode != "default" {
					c.PrepareRequest = func(got context.Context, req *llm.ChatRequest) (http.Header, func(), error) {
						calls++
						if got != ctx || req != request {
							t.Error("hook inputs changed")
						}
						cleanup := func() { cleaned++ }
						if mode == "hook-error" {
							return nil, cleanup, hookErr
						}
						if mode == "cancel" {
							cancel()
						}
						if mode == "nil-cleanup" {
							cleanup = nil
						}
						return http.Header{"Authorization": {"Bearer relay-secret"}, "X-A2gent-Session": {req.SessionID}, "X-Multi": {"a", "b"}}, cleanup, nil
					}
				}
				var err error
				if stream {
					_, err = c.ChatStream(ctx, request, func(llm.StreamEvent) error { return nil })
				} else {
					_, err = c.Chat(ctx, request)
				}
				wantErr := mode == "hook-error" || mode == "http-error" || mode == "cancel"
				if (err != nil) != wantErr {
					t.Fatalf("error=%v, want error=%v", err, wantErr)
				}
				if mode == "hook-error" && !errors.Is(err, hookErr) {
					t.Errorf("hook error not wrapped: %v", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Errorf("cancel error not wrapped: %v", err)
				}
				wantCalls, wantCleanup := 1, 1
				if mode == "default" {
					wantCalls, wantCleanup = 0, 0
				}
				if mode == "nil-cleanup" {
					wantCleanup = 0
				}
				if calls != wantCalls || cleaned != wantCleanup {
					t.Errorf("calls=%d cleanup=%d", calls, cleaned)
				}
				if (mode == "hook-error" || mode == "cancel") && sent != 0 {
					t.Error("request sent after failure")
				}
			})
		}
	}
}

func fmtTestName(stream bool, mode string) string {
	if stream {
		return "stream/" + mode
	}
	return "chat/" + mode
}

func TestDefaultProvidersDoNotLeakSessionMetadata(t *testing.T) {
	for _, baseURL := range []string{"https://api.openai.com/v1", "https://openrouter.ai/api/v1", "https://generativelanguage.googleapis.com/v1beta/openai", "http://localhost:1234/v1"} {
		for _, stream := range []bool{false, true} {
			t.Run(baseURL+"/"+fmtTestName(stream, "default"), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					wire := string(body) + r.Header.Get("X-A2gent-Session") + r.Header.Get("X-A2gent-Token")
					if strings.Contains(wire, "private-session") || strings.Contains(wire, "private-secret") || strings.Contains(wire, "private-cursor") {
						t.Errorf("internal metadata leaked: %s", wire)
					}
					if r.Header.Get("Authorization") != "Bearer provider-key" {
						t.Error("provider authorization changed")
					}
					if stream {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
					}
				}))
				defer server.Close()
				c := NewClient("provider-key", "test", baseURL)
				c.httpClient.Transport = rewriteHostTransport{target: server.URL, base: http.DefaultTransport}
				ctx := context.WithValue(context.Background(), "session_id", "private-session")
				ctx = context.WithValue(ctx, "relay_secret", "private-secret")
				req := &llm.ChatRequest{SessionID: "private-session", PromptCacheKey: "public-cache-affinity", ProviderSessionCursor: "private-cursor", Messages: []llm.Message{{Role: "user", Content: "hi"}}}
				var err error
				if stream {
					_, err = c.ChatStream(ctx, req, func(llm.StreamEvent) error { return nil })
				} else {
					_, err = c.Chat(ctx, req)
				}
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
