package scheduler

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// Scheduler manages recurring job execution
type Scheduler struct {
	store                         storage.Store
	sessionManager                *session.Manager
	llmClient                     llm.Client
	toolManager                   *tools.Manager
	toolManagerForSessionResolver func(*session.Session) *tools.Manager
	jobExecutor                   func(context.Context, *storage.RecurringJob)
	config                        *config.Config

	ticker      *time.Ticker
	stopChan    chan struct{}
	wg          sync.WaitGroup
	mu          sync.Mutex
	running     bool
	runningJobs map[string]struct{}
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
		runningJobs:    make(map[string]struct{}),
	}
}

func (s *Scheduler) SetToolManagerForSessionResolver(resolve func(*session.Session) *tools.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolManagerForSessionResolver = resolve
}

// SetJobExecutor routes scheduled loop runs through the HTTP server's job runner
// so agent configuration matches manual "Run Now" executions.
func (s *Scheduler) SetJobExecutor(executor func(context.Context, *storage.RecurringJob)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobExecutor = executor
}

func (s *Scheduler) toolManagerForSession(sess *session.Session) *tools.Manager {
	s.mu.Lock()
	resolve := s.toolManagerForSessionResolver
	s.mu.Unlock()
	if resolve != nil {
		if manager := resolve(sess); manager != nil {
			return manager
		}
	}
	return s.toolManager
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
		s.mu.Lock()
		if _, ok := s.runningJobs[job.ID]; ok {
			s.mu.Unlock()
			logging.Info("Skipping due job %s (%s): execution already in progress", job.Name, job.ID)
			continue
		}
		s.runningJobs[job.ID] = struct{}{}
		s.mu.Unlock()

		// Run each job in a separate goroutine
		s.wg.Add(1)
		go func(job *storage.RecurringJob) {
			defer func() {
				s.mu.Lock()
				delete(s.runningJobs, job.ID)
				s.mu.Unlock()
				s.wg.Done()
			}()
			s.executeJob(ctx, job)
		}(job)
	}
}

// executeJob runs a single job
func (s *Scheduler) executeJob(ctx context.Context, job *storage.RecurringJob) {
	logging.Info("Executing job: %s (%s)", job.Name, job.ID)
	now := time.Now()
	defer s.rescheduleJobAfterAttempt(job, now)

	s.mu.Lock()
	executor := s.jobExecutor
	s.mu.Unlock()
	if executor != nil {
		executor(ctx, job)
		return
	}

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
	if err := s.store.SaveJobExecution(exec); err != nil {
		logging.Error("Failed to link execution record to session for job %s: %v", job.ID, err)
	}
	if assignErr := s.assignSessionToJobProject(sess, job); assignErr != nil {
		logging.Warn("Failed to assign recurring job project for session %s: %v", sess.ID, assignErr)
	}

	// Run the agent with the job's task prompt
	providerType := s.resolveJobProviderType(job)
	model := strings.TrimSpace(job.LLMModel)
	if model == "" {
		model = s.resolveModelForProvider(providerType)
	}
	sess.Metadata["provider"] = string(providerType)
	sess.Metadata["model"] = model
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist job session provider metadata: %v", err)
	}

	contextWindow := s.resolveContextWindowForProvider(providerType, model)
	jobWorkDir := s.resolveJobWorkDir(job)
	effectiveTaskPrompt, resolveErr := jobs.ResolveTaskPrompt(job, jobWorkDir)
	if resolveErr != nil {
		logging.Error("Failed to resolve task instructions for job %s: %v", job.ID, resolveErr)
		s.failExecution(exec, sess, "Failed to resolve task instructions: "+resolveErr.Error())
		return
	}
	sess.AddUserMessage(effectiveTaskPrompt)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist job session prompt: %v", err)
	}

	ref := config.NormalizeProviderRef(string(providerType))
	provider := s.config.Providers[ref]
	sessionSettings := config.ResolveProviderSessionSettings(ref, provider)
	if envBoolDefault("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", false) {
		sessionSettings.UseProviderSession = false
	}

	agentConfig := agent.Config{
		Name:                    "job-runner",
		Provider:                string(providerType),
		Model:                   model,
		MaxSteps:                s.config.MaxSteps,
		Temperature:             s.config.Temperature,
		ContextWindow:           contextWindow,
		UseProviderSession:      sessionSettings.UseProviderSession,
		ProviderSessionIdentity: sessionSettings.ProviderSessionIdentity,
	}

	client, err := s.createLLMClient(providerType, model, jobWorkDir)
	if err != nil {
		logging.Error("Failed to initialize provider %s for job %s: %v", providerType, job.ID, err)
		s.failExecution(exec, sess, "Failed to initialize provider: "+err.Error())
		return
	}

	ag := agent.New(agentConfig, client, s.toolManagerForSession(sess), s.sessionManager)

	// Create a timeout context for job execution (default 30 minutes)
	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	output, _, err := ag.Run(jobCtx, sess, effectiveTaskPrompt)

	finishedAt := time.Now()
	exec.FinishedAt = &finishedAt

	if err != nil {
		logging.Error("Job %s failed: %v", job.ID, err)
		exec.Status = "failed"
		exec.Error = err.Error()
		if sess.Status == session.StatusRunning {
			sess.SetStatus(session.StatusFailed)
			if err := s.sessionManager.Save(sess); err != nil {
				logging.Warn("Failed to mark job session failed: %v", err)
			}
		}
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

}

func (s *Scheduler) rescheduleJobAfterAttempt(job *storage.RecurringJob, attemptedAt time.Time) {
	job.LastRunAt = &attemptedAt
	nextRun, err := s.calculateNextRun(job.ScheduleCron, attemptedAt)
	if err == nil {
		job.NextRunAt = &nextRun
		logging.Info("Job %s next run scheduled for: %s", job.Name, nextRun.Format(time.RFC3339))
	} else {
		logging.Error("Failed to calculate next run for job %s: %v", job.ID, err)
	}
	job.UpdatedAt = time.Now()

	// WHY: UPDATE-only so a delete during an in-flight run cannot be undone by INSERT upsert.
	updated, err := s.store.UpdateExistingJob(job)
	if err != nil {
		logging.Error("Failed to update job %s after execution attempt: %v", job.ID, err)
		return
	}
	if !updated {
		logging.Info("Skipped reschedule for deleted job %s (%s)", job.Name, job.ID)
	}
}

func (s *Scheduler) failExecution(exec *storage.JobExecution, sess *session.Session, message string) {
	exec.Status = "failed"
	exec.Error = message
	finishedAt := time.Now()
	exec.FinishedAt = &finishedAt

	if sess != nil {
		if strings.TrimSpace(sess.Title) == "" {
			sess.SetTitle("Recurring job failed")
		}
		if strings.TrimSpace(message) != "" {
			sess.AddAssistantMessage(message, nil)
		}
		sess.SetStatus(session.StatusFailed)
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to mark job session failed: %v", err)
		}
	}

	if err := s.store.SaveJobExecution(exec); err != nil {
		logging.Error("Failed to update execution record for job %s: %v", exec.JobID, err)
	}
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
