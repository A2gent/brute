package scheduler

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/llm/gemini"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/retry"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/robfig/cron/v3"
)

const thinkingJobIDSettingKey = "A2GENT_THINKING_JOB_ID"
const thinkingProjectID = "project-thinking"
const thinkingProjectName = "Thinking"

// Scheduler manages recurring job execution
type Scheduler struct {
	store          storage.Store
	sessionManager *session.Manager
	llmClient      llm.Client
	toolManager    *tools.Manager
	config         *config.Config

	ticker   *time.Ticker
	stopChan chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
}

// NewScheduler creates a new scheduler instance
func NewScheduler(
	store storage.Store,
	sessionManager *session.Manager,
	llmClient llm.Client,
	toolManager *tools.Manager,
	cfg *config.Config,
) *Scheduler {
	return &Scheduler{
		store:          store,
		sessionManager: sessionManager,
		llmClient:      llmClient,
		toolManager:    toolManager,
		config:         cfg,
		stopChan:       make(chan struct{}),
	}
}

// Start begins the scheduler background loop
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.ticker = time.NewTicker(1 * time.Minute)
	s.mu.Unlock()

	logging.Info("Scheduler started, checking jobs every minute")

	// Run immediately on start to catch any missed jobs
	s.checkAndRunDueJobs(ctx)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-ctx.Done():
				logging.Info("Scheduler stopping due to context cancellation")
				return
			case <-s.stopChan:
				logging.Info("Scheduler stopped")
				return
			case <-s.ticker.C:
				s.checkAndRunDueJobs(ctx)
			}
		}
	}()
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.running = false
	s.ticker.Stop()
	close(s.stopChan)
	s.wg.Wait()
}

// checkAndRunDueJobs checks for jobs that need to run and executes them
func (s *Scheduler) checkAndRunDueJobs(ctx context.Context) {
	now := time.Now()

	jobs, err := s.store.GetDueJobs(now)
	if err != nil {
		logging.Error("Failed to get due jobs: %v", err)
		return
	}

	if len(jobs) == 0 {
		return
	}

	logging.Info("Found %d due job(s) to execute", len(jobs))

	for _, job := range jobs {
		// Run each job in a separate goroutine
		s.wg.Add(1)
		go func(job *storage.RecurringJob) {
			defer s.wg.Done()
			s.executeJob(ctx, job)
		}(job)
	}
}

// executeJob runs a single job
func (s *Scheduler) executeJob(ctx context.Context, job *storage.RecurringJob) {
	logging.Info("Executing job: %s (%s)", job.Name, job.ID)
	now := time.Now()

	// Create execution record
	exec := &storage.JobExecution{
		ID:        uuid.New().String(),
		JobID:     job.ID,
		Status:    "running",
		StartedAt: now,
	}

	if err := s.store.SaveJobExecution(exec); err != nil {
		logging.Error("Failed to create execution record for job %s: %v", job.ID, err)
		return
	}

	// Create a session for this job execution
	sess, err := s.sessionManager.CreateWithJob("job-runner", job.ID)
	if err != nil {
		logging.Error("Failed to create session for job %s: %v", job.ID, err)
		exec.Status = "failed"
		exec.Error = "Failed to create session: " + err.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return
	}

	exec.SessionID = sess.ID
	if thinking, thinkErr := s.isThinkingJob(job.ID); thinkErr != nil {
		logging.Warn("Failed to check thinking job for project assignment: %v", thinkErr)
	} else if thinking {
		if assignErr := s.assignSessionToThinkingProject(sess); assignErr != nil {
			logging.Warn("Failed to assign Thinking project for session %s: %v", sess.ID, assignErr)
		}
	}

	// Run the agent with the job's task prompt
	providerType := s.resolveJobProviderType(job)
	model := s.resolveModelForProvider(providerType)
	sess.Metadata["provider"] = string(providerType)
	sess.Metadata["model"] = model
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist job session provider metadata: %v", err)
	}

	contextWindow := s.resolveContextWindowForProvider(providerType)
	effectiveTaskPrompt, resolveErr := jobs.ResolveTaskPrompt(job)
	if resolveErr != nil {
		logging.Error("Failed to resolve task instructions for job %s: %v", job.ID, resolveErr)
		exec.Status = "failed"
		exec.Error = "Failed to resolve task instructions: " + resolveErr.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return
	}

	agentConfig := agent.Config{
		Name:          "job-runner",
		Model:         model,
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: contextWindow,
	}

	client, err := s.createLLMClient(providerType, model)
	if err != nil {
		logging.Error("Failed to initialize provider %s for job %s: %v", providerType, job.ID, err)
		exec.Status = "failed"
		exec.Error = "Failed to initialize provider: " + err.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return
	}

	ag := agent.New(agentConfig, client, s.toolManager, s.sessionManager)

	// Create a timeout context for job execution (default 30 minutes)
	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	sess.AddUserMessage(effectiveTaskPrompt)

	output, _, err := ag.Run(jobCtx, sess, effectiveTaskPrompt)

	finishedAt := time.Now()
	exec.FinishedAt = &finishedAt

	if err != nil {
		logging.Error("Job %s failed: %v", job.ID, err)
		exec.Status = "failed"
		exec.Error = err.Error()
	} else {
		logging.Info("Job %s completed successfully", job.ID)
		exec.Status = "success"
		// Truncate output if too long
		if len(output) > 10000 {
			exec.Output = output[:10000] + "... (truncated)"
		} else {
			exec.Output = output
		}
	}

	// Update execution record
	if err := s.store.SaveJobExecution(exec); err != nil {
		logging.Error("Failed to update execution record for job %s: %v", job.ID, err)
	}

	// Update job's last run time and calculate next run
	job.LastRunAt = &now
	nextRun, err := s.calculateNextRun(job.ScheduleCron, now)
	if err == nil {
		job.NextRunAt = &nextRun
		logging.Info("Job %s next run scheduled for: %s", job.Name, nextRun.Format(time.RFC3339))
	}
	job.UpdatedAt = now

	if err := s.store.SaveJob(job); err != nil {
		logging.Error("Failed to update job %s after execution: %v", job.ID, err)
	}
}

func (s *Scheduler) isThinkingJob(jobID string) (bool, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		return false, err
	}
	thinkingJobID := strings.TrimSpace(settings[thinkingJobIDSettingKey])
	if thinkingJobID == "" {
		return false, nil
	}
	return thinkingJobID == strings.TrimSpace(jobID), nil
}

func (s *Scheduler) assignSessionToThinkingProject(sess *session.Session) error {
	now := time.Now()
	project := &storage.Project{
		ID:        thinkingProjectID,
		Name:      thinkingProjectName,
		IsSystem:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.SaveProject(project); err != nil {
		return err
	}
	sess.ProjectID = &project.ID
	return s.sessionManager.Save(sess)
}

func normalizeJobLLMProvider(raw string) string {
	return config.NormalizeProviderRef(raw)
}

func (s *Scheduler) resolveJobProviderType(job *storage.RecurringJob) config.ProviderType {
	if job != nil {
		provider := normalizeJobLLMProvider(job.LLMProvider)
		if provider != "" {
			return config.ProviderType(provider)
		}
	}
	return config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
}

func (s *Scheduler) resolveModelForProvider(providerType config.ProviderType) string {
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback {
		return ""
	}
	provider := s.config.Providers[string(providerType)]
	if strings.TrimSpace(provider.Model) != "" {
		return strings.TrimSpace(provider.Model)
	}
	if def := config.GetProviderDefinition(providerType); def != nil && strings.TrimSpace(def.DefaultModel) != "" {
		return strings.TrimSpace(def.DefaultModel)
	}
	return strings.TrimSpace(s.config.DefaultModel)
}

func (s *Scheduler) resolveContextWindowForProvider(providerType config.ProviderType) int {
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback {
		chain, err := s.fallbackNodesForProvider(providerType)
		if err != nil {
			return 0
		}
		minContext := 0
		for _, node := range chain {
			def := config.GetProviderDefinition(config.ProviderType(node.Provider))
			if def == nil || def.ContextWindow <= 0 {
				continue
			}
			if minContext == 0 || def.ContextWindow < minContext {
				minContext = def.ContextWindow
			}
		}
		return minContext
	}
	if def := config.GetProviderDefinition(providerType); def != nil && def.ContextWindow > 0 {
		return def.ContextWindow
	}
	return 0
}

func (s *Scheduler) createLLMClient(providerType config.ProviderType, model string) (llm.Client, error) {
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback {
		return s.createFallbackChainClient(providerType)
	}
	client, err := s.createBaseLLMClient(providerType, model)
	if err != nil {
		return nil, err
	}
	retries := s.config.LLMRetries
	if retries <= 0 {
		retries = retry.DefaultMaxRetries
	}
	return retry.Wrap(client, retry.WithMaxRetries(retries)), nil
}

func (s *Scheduler) createBaseLLMClient(providerType config.ProviderType, model string) (llm.Client, error) {
	def := config.GetProviderDefinition(providerType)
	if def == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerType)
	}
	if providerType == config.ProviderFallback {
		return nil, fmt.Errorf("fallback aggregate is not a direct provider")
	}

	provider := s.config.Providers[string(providerType)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(def.DefaultURL)
	}
	modelName := strings.TrimSpace(model)
	if modelName == "" {
		modelName = s.resolveModelForProvider(providerType)
	}

	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(providerType)
	}

	// Special handling for Anthropic: OAuth or API key
	if providerType == config.ProviderAnthropic {
		if provider.OAuth != nil && provider.OAuth.AccessToken != "" {
			// Use OAuth
			tokens := &anthropic.OAuthTokens{
				AccessToken:  provider.OAuth.AccessToken,
				RefreshToken: provider.OAuth.RefreshToken,
				ExpiresIn:    int(provider.OAuth.ExpiresAt - time.Now().Unix()),
			}
			// Note: scheduler doesn't have OAuth refresh handler, tokens must be valid
			return anthropic.NewOAuthClient(tokens, modelName, nil), nil
		}
		// Fall through to API key check below
	}

	if def.RequiresKey && apiKey == "" {
		return nil, fmt.Errorf("%s requires an API key (configure provider API key or set %s)", def.DisplayName, s.apiKeyEnvName(providerType))
	}

	switch providerType {
	case config.ProviderGoogle:
		// Google Gemini uses a dedicated client with OpenAI-compatible API + Gemini extensions
		baseURL = normalizeOpenAIBaseURL(baseURL)
		return gemini.NewClient(apiKey, modelName, baseURL), nil
	case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderOpenAI:
		// Other OpenAI-compatible providers
		baseURL = normalizeOpenAIBaseURL(baseURL)
		return lmstudio.NewClient(apiKey, modelName, baseURL), nil
	case config.ProviderAnthropic:
		// Use API key (OAuth case handled above)
		return anthropic.NewClientWithBaseURL(apiKey, modelName, baseURL), nil
	default:
		return anthropic.NewClientWithBaseURL(apiKey, modelName, baseURL), nil
	}
}

func (s *Scheduler) createFallbackChainClient(providerRef config.ProviderType) (llm.Client, error) {
	chain, err := s.fallbackNodesForProvider(providerRef)
	if err != nil {
		return nil, err
	}

	nodes := make([]fallback.Node, 0, len(chain))
	for _, node := range chain {
		ptype := config.ProviderType(node.Provider)
		model := strings.TrimSpace(node.Model)
		client, err := s.createBaseLLMClient(ptype, model)
		if err != nil {
			return nil, fmt.Errorf("fallback node %s/%s is not available: %w", node.Provider, model, err)
		}
		nodes = append(nodes, fallback.Node{
			Name:   node.Provider,
			Model:  model,
			Client: client,
		})
	}
	retries := s.config.LLMRetries
	if retries <= 0 {
		retries = fallback.DefaultMaxRetries
	}
	return fallback.NewClient(nodes, fallback.WithMaxRetries(retries)), nil
}

func (s *Scheduler) apiKeyFromEnv(providerType config.ProviderType) string {
	envKey := s.apiKeyEnvName(providerType)
	if envKey != "" {
		if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
			return value
		}
	}
	if providerType == config.ProviderGoogle {
		return strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	return ""
}

func (s *Scheduler) apiKeyEnvName(providerType config.ProviderType) string {
	switch providerType {
	case config.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case config.ProviderKimi:
		return "KIMI_API_KEY"
	case config.ProviderOpenRouter:
		return "OPENROUTER_API_KEY"
	case config.ProviderGoogle:
		return "GOOGLE_API_KEY"
	case config.ProviderOpenAI:
		return "OPENAI_API_KEY"
	default:
		return ""
	}
}

func normalizeFallbackChainNodes(raw []config.FallbackChainNode) []config.FallbackChainNode {
	chain := make([]config.FallbackChainNode, 0, len(raw))
	for _, item := range raw {
		provider := config.NormalizeProviderRef(item.Provider)
		model := strings.TrimSpace(item.Model)
		if provider == "" || model == "" {
			continue
		}
		chain = append(chain, config.FallbackChainNode{Provider: provider, Model: model})
	}
	return chain
}

func normalizeOpenAIBaseURL(raw string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	switch {
	case strings.HasSuffix(baseURL, "/models"):
		baseURL = strings.TrimSuffix(baseURL, "/models")
	case strings.HasSuffix(baseURL, "/chat/completions"):
		baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	}
	return strings.TrimSpace(baseURL)
}

func legacyProvidersToFallbackNodes(raw []string, resolveModel func(config.ProviderType) string) []config.FallbackChainNode {
	nodes := make([]config.FallbackChainNode, 0, len(raw))
	for _, provider := range raw {
		normalizedProvider := config.NormalizeProviderRef(provider)
		if normalizedProvider == "" {
			continue
		}
		model := strings.TrimSpace(resolveModel(config.ProviderType(normalizedProvider)))
		if model == "" {
			continue
		}
		nodes = append(nodes, config.FallbackChainNode{Provider: normalizedProvider, Model: model})
	}
	return nodes
}

func (s *Scheduler) normalizeAndValidateFallbackChain(raw []config.FallbackChainNode) ([]config.FallbackChainNode, error) {
	chain := normalizeFallbackChainNodes(raw)
	if len(chain) < 2 {
		return nil, fmt.Errorf("fallback chain must contain at least two model nodes")
	}

	seen := make(map[string]struct{}, len(chain))
	for _, node := range chain {
		key := node.Provider + "::" + node.Model
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("fallback chain nodes must not repeat: %s/%s", node.Provider, node.Model)
		}
		seen[key] = struct{}{}

		ptype := config.ProviderType(node.Provider)
		if ptype == config.ProviderFallback {
			return nil, fmt.Errorf("fallback chain cannot include fallback_chain itself")
		}
		def := config.GetProviderDefinition(ptype)
		if def == nil {
			return nil, fmt.Errorf("unsupported provider in fallback chain: %s", node.Provider)
		}
		if !s.providerConfiguredForUse(ptype) {
			return nil, fmt.Errorf("provider %s is not configured or missing required credentials", node.Provider)
		}
	}
	return chain, nil
}

func (s *Scheduler) fallbackNodesForProvider(providerRef config.ProviderType) ([]config.FallbackChainNode, error) {
	ref := config.NormalizeProviderRef(string(providerRef))
	if ref == string(config.ProviderFallback) {
		provider := s.config.Providers[string(config.ProviderFallback)]
		if len(provider.FallbackChainNodes) > 0 {
			return s.normalizeAndValidateFallbackChain(provider.FallbackChainNodes)
		}
		return s.normalizeAndValidateFallbackChain(legacyProvidersToFallbackNodes(provider.FallbackChain, s.resolveModelForProvider))
	}
	if config.IsFallbackAggregateRef(ref) {
		id := config.FallbackAggregateIDFromRef(ref)
		for _, aggregate := range s.config.FallbackAggregates {
			if config.NormalizeToken(aggregate.ID) == id {
				return s.normalizeAndValidateFallbackChain(aggregate.Chain)
			}
		}
		return nil, fmt.Errorf("fallback aggregate not found: %s", ref)
	}
	return nil, fmt.Errorf("provider is not fallback aggregate: %s", ref)
}

func (s *Scheduler) providerConfiguredForUse(providerType config.ProviderType) bool {
	def := config.GetProviderDefinition(providerType)
	if def == nil || providerType == config.ProviderFallback {
		return false
	}
	provider := s.config.Providers[string(providerType)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(def.DefaultURL)
	}
	if baseURL == "" {
		return false
	}
	if !def.RequiresKey {
		return true
	}
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(providerType)
	}
	return apiKey != ""
}

// calculateNextRun calculates the next run time based on cron expression
func (s *Scheduler) calculateNextRun(cronExpr string, after time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(after), nil
}
