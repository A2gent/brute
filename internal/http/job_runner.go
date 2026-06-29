// job_runner.go keeps recurring-job execution helpers together after splitting server.go.
package http

import (
	"context"
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
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
	templates := s.loadPromptTemplates()
	prompt := renderPromptTemplate(templates.ScheduleToCronPromptTemplate, map[string]string{
		"schedule": scheduleText,
	})

	sess, err := s.sessionManager.Create("scheduler")
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer s.sessionManager.Delete(sess.ID)

	sess.AddUserMessage(prompt)

	providerType := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
	model := s.resolveModelForProvider(providerType)
	target, err := s.resolveExecutionTarget(ctx, providerType, model, prompt, sess)
	if err != nil {
		return "", fmt.Errorf("failed to initialize provider %s: %w", providerType, err)
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

	sess, err := s.sessionManager.CreateWithJob("job-runner", job.ID)
	if err != nil {
		exec.Status = "failed"
		exec.Error = "Failed to create session: " + err.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return exec, nil
	}
	if assignErr := s.assignSessionToJobProject(sess, job); assignErr != nil {
		logging.Warn("Failed to assign recurring job project for session %s: %v", sess.ID, assignErr)
	}

	exec.SessionID = sess.ID
	if err := s.store.SaveJobExecution(exec); err != nil {
		logging.Error("Failed to link execution record to session: %v", err)
	}

	providerType := s.resolveJobProviderType(job)
	model := s.resolveModelForProvider(providerType)
	sess.Metadata["provider"] = string(providerType)
	sess.Metadata["model"] = model
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist job session provider metadata: %v", err)
	}
	_ = s.ensureSessionSystemPromptSnapshot(sess)

	effectiveTaskPrompt, resolveErr := jobs.ResolveTaskPrompt(job, s.resolveSessionWorkDir(sess))
	if resolveErr != nil {
		s.failJobExecution(exec, sess, "Failed to resolve task instructions: "+resolveErr.Error())
		return exec, nil
	}
	sess.AddUserMessage(effectiveTaskPrompt)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist job session prompt: %v", err)
	}

	target, clientErr := s.resolveExecutionTarget(ctx, providerType, model, effectiveTaskPrompt, sess)
	if clientErr != nil {
		s.failJobExecution(exec, sess, "Failed to initialize provider: "+clientErr.Error())
		return exec, nil
	}
	if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model) {
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist job session routed target metadata: %v", err)
		}
	}

	agentConfig := agent.Config{
		Name:                "job-runner",
		Provider:            string(target.ProviderType),
		Model:               target.Model,
		SystemPrompt:        s.buildSystemPromptForSession(sess),
		MaxSteps:            s.config.MaxSteps,
		Temperature:         s.config.Temperature,
		ContextWindow:       target.ContextWindow,
		UsePreviousResponse: target.StatefulResponses,
	}
	ag := s.newAgentFromConfig(agentConfig, target.Client, s.toolManagerForSession(sess))
	output, _, err := ag.Run(ctx, sess, effectiveTaskPrompt)

	finishedAt := time.Now()
	exec.FinishedAt = &finishedAt

	if err != nil {
		exec.Status = "failed"
		exec.Error = err.Error()
		if sess.Status == session.StatusRunning {
			sess.SetStatus(session.StatusFailed)
			if err := s.sessionManager.Save(sess); err != nil {
				logging.Warn("Failed to mark job session failed: %v", err)
			}
		}
	} else {
		exec.Status = "success"
		exec.Output = output
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

	return exec, nil
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
