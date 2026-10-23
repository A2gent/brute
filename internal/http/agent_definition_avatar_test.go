package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Minimal valid PNG header; http.DetectContentType only needs the signature.
var testAvatarPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)

func TestAgentAvatarUploadListServeAndDelete(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	folder := t.TempDir()
	createTestProject(t, store, "proj-avatar", "Avatar", folder)
	agentDir := filepath.Join(folder, "agents", "reviewer")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte("version: \"1\"\nagent:\n  id: reviewer\n  name: Reviewer\nruntime:\n  type: docker\n"), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	// A stale avatar with another extension must be replaced, not shadow the upload.
	if err := os.WriteFile(filepath.Join(agentDir, "avatar.webp"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old avatar: %v", err)
	}

	router := chi.NewRouter()
	router.Get("/unified-agents/{agentDefID}/avatar", server.handleGetAgentAvatar)
	router.Put("/unified-agents/{agentDefID}/avatar", server.handleUploadAgentAvatar)
	router.Delete("/unified-agents/{agentDefID}/avatar", server.handleDeleteAgentAvatar)
	do := func(method, target string, body []byte) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, target, bytes.NewReader(body)))
		return rec
	}

	if rec := do(http.MethodPut, "/unified-agents/reviewer/avatar?project_id=proj-avatar", []byte("not an image")); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-image upload status = %d, want 400", rec.Code)
	}
	if rec := do(http.MethodPut, "/unified-agents/reviewer/avatar?project_id=proj-avatar", testAvatarPNG); rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(agentDir, "avatar.webp")); !os.IsNotExist(err) {
		t.Fatalf("old avatar.webp should be removed, stat err = %v", err)
	}

	listRec := httptest.NewRecorder()
	server.handleListUnifiedAgents(listRec, httptest.NewRequest(http.MethodGet, "/unified-agents/?project_id=proj-avatar", nil))
	var listResp struct {
		Agents []UnifiedAgentResponse `json:"agents"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	avatarURL := ""
	for _, agent := range listResp.Agents {
		if agent.ID == "reviewer" {
			avatarURL = agent.AvatarURL
		}
	}
	if !strings.HasPrefix(avatarURL, "/unified-agents/reviewer/avatar?") || !strings.Contains(avatarURL, "project_id=proj-avatar") {
		t.Fatalf("avatar_url = %q", avatarURL)
	}

	getRec := do(http.MethodGet, avatarURL, nil)
	if getRec.Code != http.StatusOK || !bytes.Equal(getRec.Body.Bytes(), testAvatarPNG) {
		t.Fatalf("get avatar status = %d, body len = %d", getRec.Code, getRec.Body.Len())
	}

	if rec := do(http.MethodDelete, "/unified-agents/reviewer/avatar?project_id=proj-avatar", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/unified-agents/reviewer/avatar?project_id=proj-avatar", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", rec.Code)
	}
}
