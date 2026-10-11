package http

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/storage"
	"net/http"
	"testing"
)

func TestProjectTaskImagesRoundTrip(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()
	project := &storage.Project{ID: "images-project", Name: "Images"}
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	images := []map[string]string{{"name": "one.png", "media_type": "image/png", "data_base64": "b25l"}, {"name": "two.png", "media_type": "image/png", "data_base64": "dHdv"}}
	rec := requestProjectJSON(t, server, http.MethodPost, "/projects/images-project/tasks", map[string]any{"title": "Later", "images": images, "status": "todo"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var task storage.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if len(task.Images) != 2 || task.SessionID != "" || task.Status != "todo" {
		t.Fatalf("task = %#v", task)
	}
	rec = requestProjectJSON(t, server, http.MethodPatch, "/projects/images-project/tasks/"+task.Ref, map[string]any{"images": []any{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetTask(project.ID, task.Ref)
	if err != nil || len(loaded.Images) != 0 || loaded.Image != nil {
		t.Fatalf("clear = %#v, %v", loaded, err)
	}
	for _, invalid := range []any{[]map[string]string{{"media_type": "image/png", "data_base64": "not-base64"}}, make([]map[string]string, 9)} {
		rec = requestProjectJSON(t, server, http.MethodPost, "/projects/images-project/tasks", map[string]any{"title": "Invalid", "images": invalid})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid images accepted: %d", rec.Code)
		}
	}
}
