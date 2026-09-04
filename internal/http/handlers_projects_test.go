package http

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestProjectURLPatternsRoundTripThroughProjectAPI(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	createPayload := CreateProjectRequest{
		Name: "URL Project",
		URLPatterns: []string{
			" https://example.com/* ",
			"https://*.example.test/app/*",
			"https://example.com/*",
		},
	}
	createRec := requestProjectJSON(t, server, stdhttp.MethodPost, "/projects", createPayload)
	if createRec.Code != stdhttp.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", createRec.Code, createRec.Body.String())
	}

	var created ProjectResponse
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	wantPatterns := []string{"https://example.com/*", "https://*.example.test/app/*"}
	if !reflect.DeepEqual(created.URLPatterns, wantPatterns) {
		t.Fatalf("created URLPatterns = %#v, want %#v", created.URLPatterns, wantPatterns)
	}

	renameRec := requestProjectJSON(t, server, stdhttp.MethodPut, "/projects/"+created.ID, UpdateProjectRequest{
		Name: stringPtr("Renamed URL Project"),
	})
	if renameRec.Code != stdhttp.StatusOK {
		t.Fatalf("rename project status = %d, body = %s", renameRec.Code, renameRec.Body.String())
	}

	var renamed ProjectResponse
	if err := json.NewDecoder(renameRec.Body).Decode(&renamed); err != nil {
		t.Fatalf("decode rename response: %v", err)
	}
	if renamed.Name != "Renamed URL Project" {
		t.Fatalf("renamed project name = %q, want %q", renamed.Name, "Renamed URL Project")
	}
	if !reflect.DeepEqual(renamed.URLPatterns, wantPatterns) {
		t.Fatalf("renamed URLPatterns = %#v, want %#v", renamed.URLPatterns, wantPatterns)
	}

	updatePatterns := []string{"https://docs.example.com/*"}
	updateRec := requestProjectJSON(t, server, stdhttp.MethodPut, "/projects/"+created.ID, UpdateProjectRequest{
		URLPatterns: &updatePatterns,
	})
	if updateRec.Code != stdhttp.StatusOK {
		t.Fatalf("update patterns status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}

	getReq := httptest.NewRequest(stdhttp.MethodGet, "/projects/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	server.router.ServeHTTP(getRec, getReq)
	if getRec.Code != stdhttp.StatusOK {
		t.Fatalf("get project status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	var got ProjectResponse
	if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if !reflect.DeepEqual(got.URLPatterns, updatePatterns) {
		t.Fatalf("got URLPatterns = %#v, want %#v", got.URLPatterns, updatePatterns)
	}
}

func TestCreateProjectClonesRepositoryIntoSelectedFolder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	remoteDir := createProjectCloneRemote(t)
	projectDir := filepath.Join(t.TempDir(), "cloned-project")
	createRec := requestProjectJSON(t, server, stdhttp.MethodPost, "/projects", CreateProjectRequest{
		Name:          "Cloned Project",
		Folder:        stringPtr(projectDir),
		RepositoryURL: remoteDir,
	})
	if createRec.Code != stdhttp.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", createRec.Code, createRec.Body.String())
	}

	readme, err := os.ReadFile(filepath.Join(projectDir, "README.md"))
	if err != nil {
		t.Fatalf("read cloned file: %v", err)
	}
	if string(readme) != "cloned content\n" {
		t.Fatalf("README.md = %q, want cloned content", string(readme))
	}
	origin, err := runGitCommand(projectDir, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("read cloned origin: %v", err)
	}
	if origin != remoteDir {
		t.Fatalf("origin = %q, want %q", origin, remoteDir)
	}
}

func TestCreateProjectRejectsCloneIntoNonEmptyFolderWithoutSavingProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}
	createRec := requestProjectJSON(t, server, stdhttp.MethodPost, "/projects", CreateProjectRequest{
		Name:          "Unsafe Clone",
		Folder:        stringPtr(projectDir),
		RepositoryURL: createProjectCloneRemote(t),
	})
	if createRec.Code != stdhttp.StatusBadRequest {
		t.Fatalf("create project status = %d, want %d; body = %s", createRec.Code, stdhttp.StatusBadRequest, createRec.Body.String())
	}
	projects, err := store.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for _, project := range projects {
		if project.Name == "Unsafe Clone" {
			t.Fatalf("project was saved after clone validation failed")
		}
	}
}

func createProjectCloneRemote(t *testing.T) string {
	t.Helper()

	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	workDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("create source folder: %v", err)
	}
	if _, err := runGitCommand(workDir, "init"); err != nil {
		t.Fatalf("init source repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("cloned content\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if _, err := runGitCommand(workDir, "add", "README.md"); err != nil {
		t.Fatalf("stage source file: %v", err)
	}
	if _, err := runGitCommand(workDir, "-c", "user.name=A2gent Test", "-c", "user.email=test@a2gent.local", "commit", "-m", "Initial commit"); err != nil {
		t.Fatalf("commit source file: %v", err)
	}
	if _, err := runGitCommand(workDir, "clone", "--bare", ".", remoteDir); err != nil {
		t.Fatalf("create bare remote: %v", err)
	}
	return remoteDir
}

func newProjectsAPITestServer(t *testing.T) (*Server, *storage.SQLiteStore) {
	t.Helper()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	return server, store
}

func requestProjectJSON(t *testing.T, server *Server, method string, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	return rec
}

func stringPtr(value string) *string {
	return &value
}
