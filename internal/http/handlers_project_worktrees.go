// handlers_project_worktrees.go manages extra git worktrees of a project folder.
// WHY: a worktree is a second working folder of the same repository with its own
// checkout, so the user (and later parallel sessions) can test another branch
// without switching the branch of the main project folder.
package http

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/go-chi/chi/v5"
)

const (
	projectWorktreesEnabledSettingKey   = "A2GENT_PROJECT_WORKTREES_ENABLED"
	projectWorktreesDirectorySettingKey = "A2GENT_PROJECT_WORKTREES_DIRECTORY"
	projectWorktreesBaseRefSettingKey   = "A2GENT_PROJECT_WORKTREES_BASE_REF"
)

type ProjectWorktree struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Main     bool   `json:"main"`
	Prunable bool   `json:"prunable"`
}

type CreateProjectWorktreeRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleListProjectWorktrees(w http.ResponseWriter, r *http.Request) {
	root, err := s.resolveProjectRootFolder(chi.URLParam(r, "projectID"))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.respondProjectWorktrees(w, root, http.StatusOK)
}

func (s *Server) handleCreateProjectWorktree(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	root, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	settings := normalizeProjectSettings(project.Settings)
	if settings[projectWorktreesEnabledSettingKey] != "true" {
		s.errorResponse(w, http.StatusBadRequest, "Worktrees are disabled for this project; enable them and save the project settings first")
		return
	}

	var req CreateProjectWorktreeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		s.errorResponse(w, http.StatusBadRequest, "Worktree name must be a plain folder name")
		return
	}

	baseRef := settings[projectWorktreesBaseRefSettingKey]
	if baseRef == "" {
		baseRef = "origin/HEAD"
	}
	// WHY: a new worktree must start from the latest remote state, not from whatever
	// the main folder has checked out. Being offline must not block creation.
	if _, err := runGitCommand(root, "fetch", "origin"); err != nil {
		logging.Warn("Worktree fetch failed, using local refs: project=%s error=%v", projectID, err)
	}
	// --detach keeps the base branch free, so it can still be checked out in the main
	// folder; --relative-paths keeps the link valid when mounted into Docker agents.
	path := filepath.Join(projectWorktreesDirectory(root, settings[projectWorktreesDirectorySettingKey]), name)
	if _, err := runGitCommand(root, "worktree", "add", "--relative-paths", "--detach", path, baseRef); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to create worktree: "+err.Error())
		return
	}
	s.respondProjectWorktrees(w, root, http.StatusCreated)
}

func (s *Server) handleDeleteProjectWorktree(w http.ResponseWriter, r *http.Request) {
	root, err := s.resolveProjectRootFolder(chi.URLParam(r, "projectID"))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	// git refuses to remove the main folder or a worktree with uncommitted changes.
	if _, err := runGitCommand(root, "worktree", "remove", r.URL.Query().Get("path")); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to remove worktree: "+err.Error())
		return
	}
	s.respondProjectWorktrees(w, root, http.StatusOK)
}

func (s *Server) respondProjectWorktrees(w http.ResponseWriter, root string, status int) {
	output, err := runGitCommand(root, "worktree", "list", "--porcelain")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to list worktrees: "+err.Error())
		return
	}
	worktrees := []ProjectWorktree{}
	for i, block := range strings.Split(output, "\n\n") {
		worktree := ProjectWorktree{Main: i == 0}
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				worktree.Path = value
			case "HEAD":
				worktree.Head = value
			case "branch":
				worktree.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "detached":
				worktree.Detached = true
			case "prunable":
				worktree.Prunable = true
			}
		}
		worktrees = append(worktrees, worktree)
	}
	s.jsonResponse(w, status, worktrees)
}

// projectWorktreesDirectory defaults to a sibling "<project>-worktrees" folder so
// worktrees never show up inside the main folder's file tree, search or tests.
func projectWorktreesDirectory(root string, configured string) string {
	if configured == "" {
		return root + "-worktrees"
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(root, configured)
}
