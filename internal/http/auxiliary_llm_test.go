package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestAuxiliaryRequestsUseLowestReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		model  string
		effort string
		budget int
	}{
		{"gpt-5.4-mini", "none", 80},
		{"gpt-4.1", "", 80},
		{"gpt-5-mini", "minimal", 2048},
	} {
		t.Run(tc.model, func(t *testing.T) {
			var body map[string]interface{}
			text := strings.Repeat("Summary word ", 50)
			upstream := newReasoningTestCodexServer(t, &body, text)
			cfg := config.DefaultConfig()
			cfg.ActiveProvider = string(config.ProviderOpenAI)
			provider := config.Provider{APIKey: "test", BaseURL: upstream.URL, Model: tc.model}
			if tc.effort != "" {
				provider.ReasoningEffort = "high"
			}
			cfg.Providers[string(config.ProviderOpenAI)] = provider
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			s := &Server{config: cfg, store: store}

			for _, kind := range []string{"summary", "commit", "pr", "meeting"} {
				t.Run(kind, func(t *testing.T) {
					body = nil
					if _, err := callAuxiliaryForTest(t, s, kind, text); err != nil {
						t.Fatal(err)
					}
					if tc.effort == "" {
						if _, ok := body["reasoning"]; ok {
							t.Fatalf("non-reasoning model received reasoning: %v", body)
						}
					} else {
						assertReasoningEffort(t, body, tc.effort)
					}
					if kind == "summary" || kind == "meeting" {
						if body["service_tier"] != "flex" {
							t.Errorf("background service_tier = %v, want flex", body["service_tier"])
						}
					} else if _, ok := body["service_tier"]; ok {
						t.Errorf("interactive request received service_tier: %v", body)
					}
					if kind == "summary" && body["max_output_tokens"] != float64(tc.budget) {
						t.Errorf("summary token budget = %v, want %d", body["max_output_tokens"], tc.budget)
					}
				})
			}
		})
	}
}

func callAuxiliaryForTest(t *testing.T, s *Server, kind, text string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	switch kind {
	case "summary":
		summary := s.generateSessionSummary(ctx, &session.Session{}, "Summarize this session")
		if summary == "" {
			return "", fmt.Errorf("empty session summary")
		}
		if text != "" && summary != session.SummaryFromContent(text) {
			t.Errorf("summary trimming changed: %q", summary)
		}
		return summary, nil
	case "meeting":
		return s.generateMeetingSummary(ctx, meetingHistoryItem{Title: "Meeting", TranscriptMarkdown: "Notes"})
	case "commit":
		response, err := s.generateGitCommitMessageWithProvider(ctx, config.ProviderOpenAI, "", "Generate a commit message")
		if err != nil {
			return "", err
		}
		return response.Content, nil
	case "pr":
		response, err := s.generateGitPRDescriptionWithProvider(ctx, config.ProviderOpenAI, "", "Generate a PR description")
		if err != nil {
			return "", err
		}
		return response.Content, nil
	default:
		t.Fatalf("unknown auxiliary kind %s", kind)
		return "", nil
	}
}

func TestBackgroundAuxiliaryFlexFallback(t *testing.T) {
	for _, kind := range []string{"summary", "meeting"} {
		for _, tc := range []struct {
			name      string
			provider  config.ProviderType
			status    int
			failBoth  bool
			wantCalls int
			wantError bool
		}{
			{"resource unavailable", config.ProviderOpenAI, 429, false, 2, false},
			{"retry fails", config.ProviderOpenAI, 429, true, 2, true},
			{"bad request", config.ProviderOpenAI, 400, false, 1, true},
			{"server error", config.ProviderOpenAI, 500, false, 1, true},
			{"codex is not flex", config.ProviderOpenAICodex, 429, false, 1, true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				requests := make(chan map[string]interface{}, 10)
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					requests <- body
					calls++
					if calls == 1 || tc.failBoth {
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(`{"error":{"code":"resource_unavailable","message":"Resource unavailable"}}`))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Summary"}]}]}`))
				}))
				t.Cleanup(upstream.Close)
				cfg := config.DefaultConfig()
				cfg.ActiveProvider = string(tc.provider)
				cfg.LLMRetries = 0
				if tc.failBoth {
					cfg.LLMRetries = 3
				}
				// A request-level auto override must also escape a flex provider default.
				cfg.Providers[string(tc.provider)] = config.Provider{
					APIKey: "test", BaseURL: upstream.URL, Model: "gpt-5.4-mini", ReasoningEffort: "high", ServiceTier: "flex",
				}
				store, err := storage.NewSQLiteStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				s := &Server{config: cfg, store: store}
				_, err = callAuxiliaryForTest(t, s, kind, "")
				if (err != nil) != tc.wantError {
					t.Errorf("error = %v, want error %v", err, tc.wantError)
				}
				if len(requests) != tc.wantCalls {
					t.Fatalf("requests = %d, want %d", len(requests), tc.wantCalls)
				}
				first := <-requests
				if tc.provider == config.ProviderOpenAI {
					assertReasoningEffort(t, first, "none")
					if first["service_tier"] != "flex" {
						t.Errorf("initial service_tier = %v, want flex", first["service_tier"])
					}
				} else {
					assertReasoningEffort(t, first, "high")
				}
				if tc.wantCalls == 2 {
					second := <-requests
					if second["service_tier"] != "auto" {
						t.Errorf("retry service_tier = %v, want auto", second["service_tier"])
					}
					assertReasoningEffort(t, second, "none")
				}
			})
		}
	}
}

func TestAuxiliaryReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		provider config.ProviderType
		model    string
		want     string
	}{
		{config.ProviderOpenAI, "gpt-5", "minimal"},
		{config.ProviderOpenAI, "gpt-5-mini", "minimal"},
		{config.ProviderOpenAI, "gpt-5-nano-2025-08-07", "minimal"},
		{config.ProviderOpenAI, "gpt-5.1", "none"},
		{config.ProviderOpenAI, "gpt-5.4-mini", "none"},
		{config.ProviderOpenAI, " GPT-5.4-NANO-2026-03-17 ", "none"},
		{config.ProviderOpenAI, "gpt-5.6-sol", "none"},
		{config.ProviderOpenAI, "gpt-6-sol", "none"},
		{config.ProviderOpenAI, "gpt-6-luna", "none"},
		{config.ProviderOpenAI, "gpt-6-astra", "low"},
		{config.ProviderOpenAI, "gpt-6.1-sol", "low"},
		{config.ProviderOpenAI, "gpt-5.2-codex", "low"},
		{config.ProviderOpenAI, "gpt-5.1-codex-max", "low"},
		{config.ProviderOpenAI, "gpt-5.1-codex-mini", "medium"},
		{config.ProviderOpenAI, "gpt-5-chat-latest", ""},
		{config.ProviderOpenAI, "gpt-5.1-chat-latest", ""},
		{config.ProviderOpenAI, "gpt-5-pro", "high"},
		{config.ProviderOpenAI, "gpt-5.4-pro", "medium"},
		{config.ProviderOpenAI, "gpt-4.1", ""},
		{config.ProviderOpenAI, "gpt-4o", ""},
		{config.ProviderOpenAI, "gpt-50", ""},
		{config.ProviderOpenAI, "", ""},
		{config.ProviderOpenAICodex, "gpt-5.4-mini", ""},
		{config.ProviderOpenRouter, "openai/gpt-5.4-mini", ""},
		{config.ProviderAnthropic, "gpt-5.4-mini", ""},
	} {
		t.Run(string(tc.provider)+"/"+tc.model, func(t *testing.T) {
			if got := auxiliaryReasoningEffort(tc.provider, tc.model); got != tc.want {
				t.Errorf("effort = %q, want %q", got, tc.want)
			}
			wantTier := ""
			if tc.provider == config.ProviderOpenAI {
				wantTier = "flex"
			}
			if got := auxiliaryBackgroundServiceTier(tc.provider); got != wantTier {
				t.Errorf("background tier = %q, want %q", got, wantTier)
			}
		})
	}
}

func TestAuxiliaryRouterReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		model      string
		configured string
		want       string
	}{
		{"gpt-5.4-mini", "", "none"},
		{"gpt-4.1", "", ""},
		{"gpt-5.4-mini", "medium", "medium"},
	} {
		t.Run(tc.model+"/"+tc.configured, func(t *testing.T) {
			var body map[string]interface{}
			upstream := newReasoningTestCodexServer(t, &body, `{"index":1,"reason":"coding"}`)
			cfg := config.DefaultConfig()
			provider := config.Provider{APIKey: "test", BaseURL: upstream.URL, Model: tc.model}
			if tc.want != "" {
				provider.ReasoningEffort = "high"
			}
			cfg.Providers[string(config.ProviderOpenAI)] = provider
			s := &Server{config: cfg}
			// Leave RouterModel empty to exercise provider-model resolution too.
			_, _, err := s.selectRoutingRuleViaLLM(t.Context(), "fix tests", config.Provider{
				RouterProvider: string(config.ProviderOpenAI), RouterReasoningEffort: tc.configured,
			}, []config.RouterRule{{Match: "coding", Provider: string(config.ProviderOpenAI), Model: tc.model}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != "" {
				assertReasoningEffort(t, body, tc.want)
			} else if _, ok := body["reasoning"]; ok {
				t.Errorf("non-reasoning router received reasoning: %v", body)
			}
			if _, ok := body["service_tier"]; ok {
				t.Errorf("router received background tier: %v", body)
			}
		})
	}
}
