// handlers_jobs.go keeps recurring-job HTTP handlers focused without changing behavior.
package http

import (
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) normalizeJobProjectID(raw string) (*string, error) {
	projectID := strings.TrimSpace(raw)
	if projectID == "" {
		return nil, nil
	}
	if _, err := s.store.GetProject(projectID); err != nil {
		return nil, fmt.Errorf("Project not found: %w", err)
	}
	return &projectID, nil
}

func jobMatchesProject(job *storage.RecurringJob, projectID string) bool {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return true
	}
	if job == nil || job.ProjectID == nil {
		return false
	}
	return strings.TrimSpace(*job.ProjectID) == projectID
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	jobs, err := s.store.ListJobs()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list jobs: "+err.Error())
		return
	}

	resp := make([]JobResponse, 0, len(jobs))
	for _, job := range jobs {
		if !jobMatchesProject(job, projectID) {
			continue
		}
		resp = append(resp, s.jobToResponse(job))
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req CreateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return
	}
	if req.ScheduleText == "" {
		s.errorResponse(w, http.StatusBadRequest, "Schedule text is required")
		return
	}

	projectID, projectErr := s.normalizeJobProjectID(req.ProjectID)
	if projectErr != nil {
		s.errorResponse(w, http.StatusBadRequest, projectErr.Error())
		return
	}

	taskPromptSource := jobs.NormalizeTaskPromptSource(req.TaskPromptSource)
	taskPromptFile := strings.TrimSpace(req.TaskPromptFile)
	taskPrompt := strings.TrimSpace(req.TaskPrompt)
	if taskPromptSource == jobs.TaskPromptSourceFile {
		if taskPromptFile == "" {
			s.errorResponse(w, http.StatusBadRequest, "Task prompt file is required when source is file")
			return
		}
		taskPrompt = jobs.BuildTaskPromptForFile(taskPromptFile)
	} else if taskPrompt == "" {
		s.errorResponse(w, http.StatusBadRequest, "Task prompt is required")
		return
	}

	llmProvider := normalizeJobLLMProvider(req.LLMProvider)
	if llmProvider != "" {
		if err := s.validateProviderRefForExecution(llmProvider); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Unsupported LLM provider: "+llmProvider+" ("+err.Error()+")")
			return
		}
	}

	cronExpr, err := s.parseScheduleToCron(r.Context(), req.ScheduleText)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to parse schedule: "+err.Error())
		return
	}

	now := time.Now()
	job := &storage.RecurringJob{
		ID:               uuid.New().String(),
		ProjectID:        projectID,
		Name:             req.Name,
		ScheduleHuman:    req.ScheduleText,
		ScheduleCron:     cronExpr,
		TaskPrompt:       taskPrompt,
		TaskPromptSource: taskPromptSource,
		TaskPromptFile:   taskPromptFile,
		LLMProvider:      llmProvider,
		Enabled:          req.Enabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	nextRun, err := s.calculateNextRun(cronExpr, now)
	if err == nil {
		job.NextRunAt = &nextRun
	}

	if err := s.store.SaveJob(job); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save job: "+err.Error())
		return
	}

	logging.Info("Created recurring job: %s (%s)", job.Name, job.ID)
	s.jsonResponse(w, http.StatusCreated, s.jobToResponse(job))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := s.store.GetJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, s.jobToResponse(job))
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := s.store.GetJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}

	var req UpdateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.ProjectID != nil {
		projectID, projectErr := s.normalizeJobProjectID(*req.ProjectID)
		if projectErr != nil {
			s.errorResponse(w, http.StatusBadRequest, projectErr.Error())
			return
		}
		job.ProjectID = projectID
	}

	if req.Name != "" {
		job.Name = req.Name
	}
	if req.Enabled != nil {
		job.Enabled = *req.Enabled
	}
	if req.LLMProvider != nil {
		llmProvider := normalizeJobLLMProvider(*req.LLMProvider)
		if llmProvider != "" {
			if err := s.validateProviderRefForExecution(llmProvider); err != nil {
				s.errorResponse(w, http.StatusBadRequest, "Unsupported LLM provider: "+llmProvider+" ("+err.Error()+")")
				return
			}
		}
		job.LLMProvider = llmProvider
	}
	taskPromptSource := job.TaskPromptSource
	if req.TaskPromptSource != "" {
		taskPromptSource = jobs.NormalizeTaskPromptSource(req.TaskPromptSource)
	}
	taskPromptFile := job.TaskPromptFile
	if req.TaskPromptFile != "" {
		taskPromptFile = strings.TrimSpace(req.TaskPromptFile)
	}
	taskPrompt := job.TaskPrompt
	if req.TaskPrompt != "" {
		taskPrompt = strings.TrimSpace(req.TaskPrompt)
	}
	if taskPromptSource == jobs.TaskPromptSourceFile {
		if strings.TrimSpace(taskPromptFile) == "" {
			s.errorResponse(w, http.StatusBadRequest, "Task prompt file is required when source is file")
			return
		}
		taskPrompt = jobs.BuildTaskPromptForFile(taskPromptFile)
	} else if strings.TrimSpace(taskPrompt) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Task prompt is required")
		return
	}
	job.TaskPromptSource = taskPromptSource
	job.TaskPromptFile = strings.TrimSpace(taskPromptFile)
	job.TaskPrompt = strings.TrimSpace(taskPrompt)

	if req.ScheduleText != "" && req.ScheduleText != job.ScheduleHuman {
		cronExpr, err := s.parseScheduleToCron(r.Context(), req.ScheduleText)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Failed to parse schedule: "+err.Error())
			return
		}
		job.ScheduleHuman = req.ScheduleText
		job.ScheduleCron = cronExpr

		nextRun, err := s.calculateNextRun(cronExpr, time.Now())
		if err == nil {
			job.NextRunAt = &nextRun
		}
	}

	job.UpdatedAt = time.Now()

	if err := s.store.SaveJob(job); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update job: "+err.Error())
		return
	}

	logging.Info("Updated recurring job: %s (%s)", job.Name, job.ID)
	s.jsonResponse(w, http.StatusOK, s.jobToResponse(job))
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	protected, err := s.isProtectedThinkingJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to check protected jobs: "+err.Error())
		return
	}
	if protected {
		s.errorResponse(w, http.StatusForbidden, "This job is managed by Thinking settings and cannot be deleted directly.")
		return
	}

	if err := s.store.DeleteJob(jobID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete job: "+err.Error())
		return
	}

	logging.Info("Deleted recurring job: %s", jobID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) isProtectedThinkingJob(jobID string) (bool, error) {
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

func (s *Server) handleRunJobNow(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := s.store.GetJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}

	exec, err := s.executeJob(r.Context(), job)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to execute job: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, s.executionToResponse(exec))
}

func (s *Server) handleListJobExecutions(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	limit := 20
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	executions, err := s.store.ListJobExecutions(jobID, limit)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list executions: "+err.Error())
		return
	}

	resp := make([]JobExecutionResponse, len(executions))
	for i, exec := range executions {
		resp[i] = s.executionToResponse(exec)
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleListJobSessions(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	_, err := s.store.GetJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}

	sessions, err := s.store.ListSessionsByJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sessions: "+err.Error())
		return
	}

	resp := make([]SessionListItem, len(sessions))
	for i, sess := range sessions {
		provider, model := storageSessionProviderAndModel(sess)
		routedProvider, routedModel := storageSessionRoutedProviderAndModel(sess)
		projectID := ""
		if sess.ProjectID != nil {
			projectID = *sess.ProjectID
		}
		jobID := ""
		if sess.JobID != nil {
			jobID = *sess.JobID
		}
		resp[i] = SessionListItem{
			ID:                 sess.ID,
			AgentID:            sess.AgentID,
			JobID:              jobID,
			ProjectID:          projectID,
			Provider:           provider,
			Model:              model,
			RoutedProvider:     routedProvider,
			RoutedModel:        routedModel,
			Title:              sess.Title,
			Summary:            sess.Summary,
			Status:             sess.Status,
			TotalTokens:        storageSessionTotalTokens(sess),
			RunDurationSeconds: sessionRunDurationSeconds(sess.CreatedAt, sess.UpdatedAt, sess.Status),
			TaskProgress:       sess.TaskProgress,
			CreatedAt:          sess.CreatedAt,
			UpdatedAt:          sess.UpdatedAt,
		}
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

// jobToResponse converts a storage job to API response
func (s *Server) jobToResponse(job *storage.RecurringJob) JobResponse {
	projectID := ""
	if job.ProjectID != nil {
		projectID = strings.TrimSpace(*job.ProjectID)
	}
	return JobResponse{
		ID:               job.ID,
		ProjectID:        projectID,
		Name:             job.Name,
		ScheduleHuman:    job.ScheduleHuman,
		ScheduleCron:     job.ScheduleCron,
		TaskPrompt:       job.TaskPrompt,
		TaskPromptSource: jobs.NormalizeTaskPromptSource(job.TaskPromptSource),
		TaskPromptFile:   strings.TrimSpace(job.TaskPromptFile),
		LLMProvider:      job.LLMProvider,
		Enabled:          job.Enabled,
		LastRunAt:        job.LastRunAt,
		NextRunAt:        job.NextRunAt,
		CreatedAt:        job.CreatedAt,
		UpdatedAt:        job.UpdatedAt,
	}
}

// executionToResponse converts a storage execution to API response
func (s *Server) executionToResponse(exec *storage.JobExecution) JobExecutionResponse {
	return JobExecutionResponse{
		ID:         exec.ID,
		JobID:      exec.JobID,
		SessionID:  exec.SessionID,
		Status:     exec.Status,
		Output:     exec.Output,
		Error:      exec.Error,
		StartedAt:  exec.StartedAt,
		FinishedAt: exec.FinishedAt,
	}
}
