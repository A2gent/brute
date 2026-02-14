package scheduler

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gratheon/aagent/internal/agent"
	"github.com/gratheon/aagent/internal/config"
	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/logging"
	"github.com/gratheon/aagent/internal/session"
	"github.com/gratheon/aagent/internal/storage"
	"github.com/gratheon/aagent/internal/tools"
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
	providerType := config.ProviderType(strings.TrimSpace(s.config.ActiveProvider))
	contextWindow := 0
	if def := config.GetProviderDefinition(providerType); def != nil {
		contextWindow = def.ContextWindow
	}

	agentConfig := agent.Config{
		Name:          "job-runner",
		Model:         s.config.DefaultModel,
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: contextWindow,
	}

	ag := agent.New(agentConfig, s.llmClient, s.toolManager, s.sessionManager)

	// Create a timeout context for job execution (default 30 minutes)
	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	output, _, err := ag.Run(jobCtx, sess, job.TaskPrompt)

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
		Folders:   []string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.SaveProject(project); err != nil {
		return err
	}
	sess.ProjectID = &project.ID
	return s.sessionManager.Save(sess)
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
