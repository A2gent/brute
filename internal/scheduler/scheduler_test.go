package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/storage"
)

func TestRescheduleJobAfterAttemptDoesNotResurrectDeletedJob(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	job := &storage.RecurringJob{
		ID:               "job-reschedule-deleted",
		Name:             "task, micro",
		ScheduleHuman:    "every 20 min",
		ScheduleCron:     "*/20 * * * *",
		TaskPrompt:       "light work",
		TaskPromptSource: "text",
		Enabled:          true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	if err := store.DeleteJob(job.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	s := &Scheduler{store: store}
	s.rescheduleJobAfterAttempt(job, now)

	if _, err := store.GetJob(job.ID); err == nil {
		t.Fatal("deleted job was resurrected by rescheduleJobAfterAttempt")
	}
}

func TestCreateBaseLLMClientAcceptsOpenAICodexOAuth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAICodex)
	cfg.DefaultModel = "gpt-5.5"
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:    string(config.ProviderOpenAICodex),
		BaseURL: "https://chatgpt.com/backend-api/codex",
		Model:   "gpt-5.5",
		OAuth: &config.OAuthConfig{
			AccessToken: "oauth-token",
		},
	}

	s := &Scheduler{config: cfg}
	if _, err := s.createBaseLLMClient(config.ProviderOpenAICodex, "", "."); err != nil {
		t.Fatalf("createBaseLLMClient(openai_codex with OAuth): %v", err)
	}
}

func TestCreateLLMClientUsesParentProxyForFallbackAggregate(t *testing.T) {
	const providerRef = "fallback_chain:mid-tier"

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/v1/providers/"+providerRef+"/chat/completions"; got != want {
			t.Errorf("proxy request path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer proxy.Close()

	t.Setenv("A2GENT_PARENT_PROXY_URL", proxy.URL+"/v1")
	t.Setenv("A2GENT_PARENT_PROXY_KEY", "")

	cfg := config.DefaultConfig()
	cfg.FallbackAggregates = nil
	s := &Scheduler{config: cfg}

	client, err := s.createLLMClient(config.ProviderType(providerRef), "", ".")
	if err != nil {
		t.Fatalf("createLLMClient returned error: %v", err)
	}
	if clientType := reflect.TypeOf(client).String(); clientType != "*lmstudio.Client" {
		t.Fatalf("expected fallback aggregate to use parent proxy client, got %s", clientType)
	}

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("proxy client Chat returned error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("proxy client response = %q, want %q", resp.Content, "ok")
	}
}

func TestFallbackNodesForProviderAcceptsOpaqueAggregateThroughParentProxy(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://host.docker.internal:5445/v1")

	cfg := config.DefaultConfig()
	cfg.FallbackAggregates = nil
	s := &Scheduler{config: cfg}

	nodes, err := s.fallbackNodesForProvider(config.ProviderType("fallback_chain:mid-tier"))
	if err != nil {
		t.Fatalf("fallbackNodesForProvider returned error: %v", err)
	}
	if nodes != nil {
		t.Fatalf("fallbackNodesForProvider returned local nodes for opaque proxy aggregate: %#v", nodes)
	}
	if !s.providerConfiguredForUse(config.ProviderType("fallback_chain:mid-tier")) {
		t.Fatal("expected fallback aggregate ref to be configured through parent proxy")
	}
}
