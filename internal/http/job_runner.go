// job_runner.go keeps recurring-job execution helpers together after splitting server.go.
package http

import (
	"context"
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"strings"
	"time"
)

const defaultScheduleToCronSystemPrompt = "You convert natural-language schedules into strict 5-field cron expressions."

const defaultScheduleToCronPromptTemplate = `Convert the following natural language schedule to a standard 5-field cron expression.
Only respond with the cron expression, nothing else. No explanation, no formatting, just the cron expression.

Schedule: "{{schedule}}"

Examples:
- "every day at 7pm" -> "0 19 * * *"
- "every Monday at 9am" -> "0 9 * * 1"
- "every hour" -> "0 * * * *"
- "every weekday at 8:30am" -> "30 8 * * 1-5"
- "every 15 minutes" -> "*/15 * * * *"

Cron expression:`

// parseScheduleToCron uses the LLM to convert natural language schedule to cron expression
func (s *Server) parseScheduleToCron(ctx context.Context, scheduleText string) (string, error) {
	settings := map[string]string{}
	if s != nil && s.store != nil {
		loaded, err := s.store.GetSettings()
		if err != nil {
			logging.Warn("Failed to load settings for schedule parser: %v", err)
		} else if loaded != nil {
			settings = loaded
		}
	}
	templates := serverPromptTemplatesFromSettings(settings)
	prompt := renderPromptTemplate(templates.ScheduleToCronPromptTemplate, map[string]string{
		"schedule": scheduleText,
	})

	sess, err := s.sessionManager.Create("scheduler")
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer s.sessionManager.Delete(sess.ID)

	sess.AddUserMessage(prompt)

	targetConfig := s.resolvePromptLLMTarget(settings, promptLLMCaseScheduleToCron)
	target, err := s.resolveExecutionTarget(ctx, targetConfig.ProviderType, targetConfig.Model, prompt, sess)
	if err != nil {
		return "", fmt.Errorf("failed to initialize provider %s: %w", targetConfig.ProviderType, err)
	}

	agentConfig := agent.Config{
		Name:                "scheduler",
		Provider:            string(target.ProviderType),
		Model:               target.Model,
		SystemPrompt:        templates.ScheduleToCronSystemPrompt,
		MaxSteps:            1,
		Temperature:         0,
		ContextWindow:       target.ContextWindow,
		UsePreviousResponse: target.StatefulResponses,
	}

	ag := s.newAgentFromConfig(agentConfig, target.Client, s.toolManagerForSession(sess))
	cronExpr, _, err := ag.Run(ctx, sess, prompt)
	if err != nil {
		return "", fmt.Errorf("failed to parse schedule: %w", err)
	}

	cronExpr = strings.TrimSpace(cronExpr)

	fields := strings.Fields(cronExpr)
	if len(fields) != 5 {
		return "", fmt.Errorf("invalid cron expression: %s", cronExpr)
	}

	return cronExpr, nil
}

// calculateNextRun calculates the next run time based on cron expression
func (s *Server) calculateNextRun(cronExpr string, after time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	return schedule.Next(after), nil
}

// executeJob runs a job and returns the execution record
func (s *Server) executeJob(ctx context.Context, job *storage.RecurringJob) (*storage.JobExecution, error) {
	return s.ExecuteJob(ctx, job)
}

type preparedJobExecution struct {
	exec                *storage.JobExecution
	sess                *session.Session
	effectiveTaskPrompt string
	startedAt           time.Time
}

// ExecuteJob runs a recurring job using the same workflow/agent routing as chat sessions.
func (s *Server) ExecuteJob(ctx context.Context, job *storage.RecurringJob) (*storage.JobExecution, error) {
	prepared, err := s.prepareJobExecution(job)
	if err != nil {
		return nil, err
	}
	if prepared.effectiveTaskPrompt == "" {
		return prepared.exec, nil
	}
	s.finishJobExecution(ctx, job, prepared)
	return prepared.exec, nil
}

// StartJobExecutionAsync prepares a loop run and continues execution in the background.
// Manual "Run Now" uses this so the API can return session_id immediately for UI redirect.
func (s *Server) StartJobExecutionAsync(job *storage.RecurringJob) (*storage.JobExecution, error) {
	prepared, err := s.prepareJobExecution(job)
	if err != nil {
		return nil, err
	}
	if prepared.effectiveTaskPrompt == "" {
		return prepared.exec, nil
	}
	go s.finishJobExecution(s.sessionRunParentContext(), job, prepared)
	return prepared.exec, nil
}

func (s *Server) prepareJobExecution(job *storage.RecurringJob) (*preparedJobExecution, error) {
	now := time.Now()

	exec := &storage.JobExecution{
		ID:        uuid.New().String(),
		JobID:     job.ID,
		Status:    "running",
		StartedAt: now,
	}

	if err := s.store.SaveJobExecution(exec); err != nil {
		return nil, fmt.Errorf("failed to create execution record: %w", err)
	}

	sess, err := s.sessionManager.CreateWithJob("build", job.ID)
	if err != nil {
		exec.Status = "failed"
		exec.Error = "Failed to create session: " + err.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return &preparedJobExecution{exec: exec, startedAt: now}, nil
	}
	if assignErr := s.assignSessionToJobProject(sess, job); assignErr != nil {
		logging.Warn("Failed to assign recurring job project for session %s: %v", sess.ID, assignErr)
	}

	jobs.ApplyRunConfigToSession(sess, job)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist job session run config: %v", err)
	}
	_ = s.ensureSessionSystemPromptSnapshot(sess)

	exec.SessionID = sess.ID
	if err := s.store.SaveJobExecution(exec); err != nil {
		logging.Error("Failed to link execution record to session: %v", err)
	}

	effectiveTaskPrompt, resolveErr := jobs.ResolveTaskPrompt(job, s.resolveSessionWorkDir(sess))
	if resolveErr != nil {
		s.failJobExecution(exec, sess, "Failed to resolve task instructions: "+resolveErr.Error())
		return &preparedJobExecution{exec: exec, sess: sess, startedAt: now}, nil
	}
	sess.AddUserMessage(effectiveTaskPrompt)
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist job session prompt: %v", err)
	}

	return &preparedJobExecution{
		exec:                exec,
		sess:                sess,
		effectiveTaskPrompt: effectiveTaskPrompt,
		startedAt:           now,
	}, nil
}

func (s *Server) finishJobExecution(ctx context.Context, job *storage.RecurringJob, prepared *preparedJobExecution) {
	if prepared == nil || prepared.exec == nil || prepared.sess == nil || prepared.effectiveTaskPrompt == "" {
		return
	}

	exec := prepared.exec
	sess := prepared.sess
	effectiveTaskPrompt := prepared.effectiveTaskPrompt
	now := prepared.startedAt

	result, runErr := s.runSessionWithoutStreaming(ctx, sess, effectiveTaskPrompt)
	if finalizeErr := s.finalizeSessionRunWithoutStreaming(ctx, sess, result, runErr); finalizeErr != nil && !isCancellationError(finalizeErr) {
		s.failJobExecution(exec, sess, finalizeErr.Error())
		return
	}

	finishedAt := time.Now()
	exec.FinishedAt = &finishedAt
	if runErr != nil {
		exec.Status = "failed"
		exec.Error = runErr.Error()
	} else {
		exec.Status = "success"
		output := strings.TrimSpace(result.Content)
		if len(output) > 10000 {
			exec.Output = output[:10000] + "... (truncated)"
		} else {
			exec.Output = output
		}
	}

	if err := s.store.SaveJobExecution(exec); err != nil {
		logging.Error("Failed to update execution record: %v", err)
	}

	job.LastRunAt = &now
	nextRun, err := s.calculateNextRun(job.ScheduleCron, now)
	if err == nil {
		job.NextRunAt = &nextRun
	}
	job.UpdatedAt = now

	if err := s.store.SaveJob(job); err != nil {
		logging.Error("Failed to update job after execution: %v", err)
	}
}

func (s *Server) failJobExecution(exec *storage.JobExecution, sess *session.Session, message string) {
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
		logging.Error("Failed to update execution record: %v", err)
	}
}

func (s *Server) assignSessionToJobProject(sess *session.Session, job *storage.RecurringJob) error {
	if sess == nil || job == nil || job.ProjectID == nil {
		return nil
	}
	projectID := strings.TrimSpace(*job.ProjectID)
	if projectID == "" {
		return nil
	}
	if _, err := s.store.GetProject(projectID); err != nil {
		return fmt.Errorf("job project not found: %w", err)
	}
	sess.ProjectID = &projectID
	return s.sessionManager.Save(sess)
}
