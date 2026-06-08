package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestHandleDiscoverSkillsIgnoresNestedProjectMarkdown(t *testing.T) {
	root := t.TempDir()
	writeHTTPTestFile(t, filepath.Join(root, "AGENTS.md"), "# Root Agent\n")
	writeHTTPTestFile(t, filepath.Join(root, "notes.markdown"), "# Notes Skill\n")
	writeHTTPTestFile(t, filepath.Join(root, "installed", "SKILL.md"), "---\nname: Installed Skill\ndescription: Registry-style skill package\n---\nUse installed skill.")
	writeHTTPTestFile(t, filepath.Join(root, "installed", "README.md"), "# Installed package docs\n")
	writeHTTPTestFile(t, filepath.Join(root, "speech", "whisper", "source", "whisper.cpp", "README.md"), "# whisper.cpp\n")
	writeHTTPTestFile(t, filepath.Join(root, "category", "nested", "SKILL.md"), "# Too Deep\n")
	writeHTTPTestFile(t, filepath.Join(root, ".hidden", "SKILL.md"), "# Hidden\n")

	server := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/skills/discover?folder="+root, nil)
	rec := httptest.NewRecorder()

	server.handleDiscoverSkills(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var response SkillDiscoverResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	got := make([]string, 0, len(response.Skills))
	for _, skill := range response.Skills {
		got = append(got, skill.RelativePath)
	}
	sort.Strings(got)

	want := []string{"AGENTS.md", "installed/SKILL.md", "notes.markdown"}
	if !equalHTTPStringSlices(got, want) {
		t.Fatalf("discovered skill paths = %#v, want %#v", got, want)
	}
}

func TestHandleDeleteSkillRemovesRootMarkdownFileOnly(t *testing.T) {
	root := t.TempDir()
	skillPath := filepath.Join(root, "AGENTS.md")
	siblingPath := filepath.Join(root, "keep.md")
	writeHTTPTestFile(t, skillPath, "# Root Agent\n")
	writeHTTPTestFile(t, siblingPath, "# Keep\n")

	server := newSkillsTestServer(t, root)
	reqBody, err := json.Marshal(map[string]string{"skill_path": skillPath})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/skills/delete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	server.handleDeleteSkill(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Fatalf("root markdown file still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(siblingPath); err != nil {
		t.Fatalf("sibling file should remain after deleting top-level skill: %v", err)
	}
}

func TestHandleDeleteSkillRemovesPackagedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "installed")
	skillPath := filepath.Join(skillDir, "SKILL.md")
	writeHTTPTestFile(t, skillPath, "# Installed\n")
	writeHTTPTestFile(t, filepath.Join(skillDir, "README.md"), "# Package docs\n")

	server := newSkillsTestServer(t, root)
	reqBody, err := json.Marshal(map[string]string{"skill_path": skillPath})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/skills/delete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	server.handleDeleteSkill(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Fatalf("packaged skill directory still exists or stat failed unexpectedly: %v", err)
	}
}

func TestHandleDeleteSkillRejectsUndiscoverableMarkdown(t *testing.T) {
	root := t.TempDir()
	nestedPath := filepath.Join(root, "speech", "whisper", "README.md")
	writeHTTPTestFile(t, nestedPath, "# Nested docs\n")

	server := newSkillsTestServer(t, root)
	reqBody, err := json.Marshal(map[string]string{"skill_path": nestedPath})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/skills/delete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	server.handleDeleteSkill(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(nestedPath); err != nil {
		t.Fatalf("undiscoverable markdown should remain after rejected delete: %v", err)
	}
}

func newSkillsTestServer(t *testing.T, skillsFolder string) *Server {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := store.SaveSettings(map[string]string{skillsFolderSettingKey: skillsFolder}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}
	return &Server{store: store}
}

func writeHTTPTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func equalHTTPStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
