package http

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

const maxSessionTaskImages = 10

// buildTaskFromSession turns a session into a task draft: user messages become the
// body, attached images are carried over, and the parent session is referenced by id.
func buildTaskFromSession(sess *session.Session) storage.TaskCreate {
	var parts []string
	var images []storage.TaskImage
	for _, msg := range sess.Messages {
		if msg.Role != "user" {
			continue
		}
		if text := strings.TrimSpace(msg.Content); text != "" {
			parts = append(parts, text)
		}
		for _, img := range msg.Images {
			if img.DataBase64 == "" || len(images) >= maxSessionTaskImages {
				continue
			}
			images = append(images, storage.TaskImage{Name: img.Name, MediaType: img.MediaType, DataBase64: img.DataBase64})
		}
	}
	title := strings.TrimSpace(sess.Title)
	if title == "" && len(parts) > 0 {
		title = parts[0]
	}
	if r := []rune(title); len(r) > 120 {
		title = string(r[:120]) + "..."
	}
	if title == "" {
		title = "Session " + sess.ID
	}
	body := strings.Join(parts, "\n\n")
	if sess.ParentID != nil && *sess.ParentID != "" {
		body = fmt.Sprintf("Parent session: %s\n\n%s", *sess.ParentID, body)
	}
	return storage.TaskCreate{Title: title, Body: strings.TrimSpace(body), Images: images, Status: storage.TaskStatusTodo, Priority: 2, CreatedBy: "user"}
}

func (s *Server) handleConvertSessionToTask(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessionManager.Get(chi.URLParam(r, "sessionID"))
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	if sess.ProjectID == nil || *sess.ProjectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "Session has no project")
		return
	}
	task, err := s.store.CreateTask(*sess.ProjectID, buildTaskFromSession(sess))
	if err != nil {
		s.taskErrorResponse(w, err)
		return
	}
	// CreateTask does not accept a session link, so attach it afterwards.
	sessionID := sess.ID
	if linked, err := s.store.UpdateTask(task.ProjectID, task.Ref, storage.TaskUpdate{SessionID: &sessionID}); err == nil {
		task = linked
	}
	s.jsonResponse(w, http.StatusCreated, task)
}
