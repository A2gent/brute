package scheduler

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
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
