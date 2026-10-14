// Package providerstest checks retry parity through the runtime client factories.
package providerstest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// CheckRetries exercises real provider clients without sockets so synctest can
// advance backoff time and detect nested retry wrappers without slow tests.
func CheckRetries(t *testing.T, factory func(*config.Config, config.ProviderType) (llm.Client, error)) {
	t.Helper()
	t.Setenv("LM_STUDIO_BASE_URL", "")
	t.Setenv("LMSTUDIO_BASE_URL", "")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, tc := range []struct {
		name    string
		payload string
		retries int
		delay   time.Duration
	}{
		{"zero", `{"llm_retries":0}`, 0, 0},
		{"omitted", `{}`, 3, 14 * time.Second},
		{"positive", `{"llm_retries":2}`, 2, 6 * time.Second},
	} {
		for _, chain := range []bool{false, true} {
			mode := "standalone"
			if chain {
				mode = "fallback"
			}
			for _, stream := range []bool{false, true} {
				method := "chat"
				if stream {
					method = "stream"
				}
				t.Run(tc.name+"/"+mode+"/"+method, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						cfg := config.DefaultConfig()
						if err := json.Unmarshal([]byte(tc.payload), cfg); err != nil {
							t.Fatal(err)
						}
						cfg.DefaultModel = "primary"
						cfg.Providers[string(config.ProviderLMStudio)] = config.Provider{
							Name: string(config.ProviderLMStudio), Model: "primary", BaseURL: "http://retry-test.invalid/v1",
						}
						cfg.Providers[string(config.ProviderFallback)] = config.Provider{
							FallbackChainNodes: []config.FallbackChainNode{
								{Provider: string(config.ProviderLMStudio), Model: "primary"},
								{Provider: string(config.ProviderLMStudio), Model: "secondary"},
							},
						}
						calls := map[string]int{}
						http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
							var body struct {
								Model string `json:"model"`
							}
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Fatal(err)
							}
							calls[body.Model]++
							return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"service unavailable"}`)), Request: r}, nil
						})
						provider := config.ProviderLMStudio
						if chain {
							provider = config.ProviderFallback
						}
						client, err := factory(cfg, provider)
						if err != nil {
							t.Fatal(err)
						}
						start := time.Now()
						switches := 0
						if stream {
							sc, ok := client.(llm.StreamingClient)
							if !ok {
								t.Fatalf("%T is not streaming", client)
							}
							_, err = sc.ChatStream(context.Background(), &llm.ChatRequest{}, func(ev llm.StreamEvent) error {
								if chain && strings.HasPrefix(ev.Phase, "retry_layer_") {
									t.Errorf("nested retry wrapper: %s", ev.Phase)
								}
								if ev.Phase == "switching_provider" {
									switches++
									if ev.Attempt != tc.retries+1 || ev.MaxAttempts != tc.retries+1 {
										t.Errorf("switch attempts = %d/%d, want %d/%d", ev.Attempt, ev.MaxAttempts, tc.retries+1, tc.retries+1)
									}
								}
								return nil
							})
						} else {
							_, err = client.Chat(context.Background(), &llm.ChatRequest{})
						}
						if err == nil {
							t.Fatal("expected exhausted provider error")
						}
						if calls["primary"] != tc.retries+1 {
							t.Errorf("primary calls = %d, want %d", calls["primary"], tc.retries+1)
						}
						wantSecondary, nodes := 0, 1
						if chain {
							wantSecondary, nodes = tc.retries+1, 2
						}
						if calls["secondary"] != wantSecondary {
							t.Errorf("secondary calls = %d, want %d", calls["secondary"], wantSecondary)
						}
						if stream && chain && switches != nodes {
							t.Errorf("switch traces = %d, want %d", switches, nodes)
						}
						if elapsed := time.Since(start); elapsed != time.Duration(nodes)*tc.delay {
							t.Errorf("backoff = %v, want %v", elapsed, time.Duration(nodes)*tc.delay)
						}
					})
				})
			}
		}
	}
}
