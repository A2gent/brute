package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

type createTaskRequest struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Status     string   `json:"status"`
	Priority   *int     `json:"priority"`
	Complexity int      `json:"complexity"`
	Tags       []string `json:"tags"`
	Price      string   `json:"price"`
}

type updateTaskRequest struct {
	Title      *string   `json:"title"`
	Body       *string   `json:"body"`
	Status     *string   `json:"status"`
	Priority   *int      `json:"priority"`
	Complexity *int      `json:"complexity"`
	Tags       *[]string `json:"tags"`
	Price      *string   `json:"price"`
	Position   *float64  `json:"position"`
}

type taskImportRequest struct {
	Path string `json:"path"`
}

type taskImportStatus struct {
	SourcePath string `json:"source_path"`
	Count      int    `json:"count"`
}

func (s *Server) handleListProjectTasks(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	tasks, err := s.store.ListTasks(projectID)
	if err != nil {
		s.taskErrorResponse(w, err)
		return
	}
	s.jsonResponse(w, http.StatusOK, tasks)
}

func (s *Server) handleCreateProjectTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	priority := 2
	if req.Priority != nil {
		priority = *req.Priority
	}
	task, err := s.store.CreateTask(chi.URLParam(r, "projectID"), storage.TaskCreate{
		Title: req.Title, Body: req.Body, Status: req.Status, Priority: priority,
		Complexity: req.Complexity, Tags: req.Tags, Price: req.Price, CreatedBy: "user",
	})
	if err != nil {
		s.taskErrorResponse(w, err)
		return
	}
	s.jsonResponse(w, http.StatusCreated, task)
}

func (s *Server) handleGetProjectTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.GetTask(chi.URLParam(r, "projectID"), chi.URLParam(r, "taskRef"))
	if err != nil {
		s.taskErrorResponse(w, err)
		return
	}
	s.jsonResponse(w, http.StatusOK, task)
}

func (s *Server) handleUpdateProjectTask(w http.ResponseWriter, r *http.Request) {
	var req updateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	task, err := s.store.UpdateTask(chi.URLParam(r, "projectID"), chi.URLParam(r, "taskRef"), storage.TaskUpdate{
		Title: req.Title, Body: req.Body, Status: req.Status, Priority: req.Priority,
		Complexity: req.Complexity, Tags: req.Tags, Price: req.Price, Position: req.Position,
	})
	if err != nil {
		s.taskErrorResponse(w, err)
		return
	}
	s.jsonResponse(w, http.StatusOK, task)
}

func (s *Server) handleDeleteProjectTask(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteTask(chi.URLParam(r, "projectID"), chi.URLParam(r, "taskRef")); err != nil {
		s.taskErrorResponse(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProjectTaskImportStatus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.taskErrorResponse(w, err)
		return
	}
	status := taskImportStatus{}
	if project.Folder != nil {
		for _, candidate := range []string{"TODO.md", "todo.md", "TO-DO.md", "to-do.md"} {
			path := filepath.Join(strings.TrimSpace(*project.Folder), candidate)
			content, readErr := os.ReadFile(path)
			if readErr == nil {
				status.SourcePath = candidate
				status.Count = len(parseMarkdownTasks(string(content), strings.TrimSpace(*project.Folder)))
				break
			}
		}
	}
	s.jsonResponse(w, http.StatusOK, status)
}

func (s *Server) handleImportNextProjectTask(w http.ResponseWriter, r *http.Request) {
	var req taskImportRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}
	}
	result, err := s.importNextMarkdownTask(chi.URLParam(r, "projectID"), req.Path)
	if err != nil {
		s.taskErrorResponse(w, err)
		return
	}
	s.jsonResponse(w, http.StatusOK, result)
}

func (s *Server) taskErrorResponse(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, storage.ErrTaskNotFound) || strings.Contains(strings.ToLower(err.Error()), "project not found") {
		status = http.StatusNotFound
	}
	s.errorResponse(w, status, err.Error())
}
