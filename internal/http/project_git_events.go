package http

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// gitEventsDebounce coalesces the burst of file writes a single git command makes.
const gitEventsDebounce = 300 * time.Millisecond

// gitWatchDirs lists the dirs whose changes alter history: the git dir (HEAD,
// packed-refs), every refs/ subdir and worktrees/ (one entry per linked worktree,
// so adding/removing a worktree or moving its HEAD is seen). Refs live in the common dir.
func gitWatchDirs(repoRoot string) []string {
	dirs := []string{}
	seen := map[string]bool{}
	add := func(dir string) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(repoRoot, dir)
		}
		dir = filepath.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	for _, arg := range []string{"--git-dir", "--git-common-dir"} {
		out, err := runGitCommand(repoRoot, "rev-parse", arg)
		if err != nil {
			continue
		}
		gitDir := strings.TrimSpace(out)
		add(gitDir)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(repoRoot, gitDir)
		}
		for _, sub := range []string{"refs", "worktrees"} {
			_ = filepath.WalkDir(filepath.Join(gitDir, sub), func(path string, d os.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					add(path)
				}
				return nil
			})
		}
	}
	return dirs
}

// handleProjectGitEvents streams "git_changed" whenever the repo's HEAD or refs
// change (commit, merge, pull, checkout, or an external terminal/agent), so the
// history and worktree views refresh without polling.
func (s *Server) handleProjectGitEvents(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}
	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	repoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(repoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "not a git repository")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by the server")
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer watcher.Close()
	for _, dir := range gitWatchDirs(repoRoot) {
		_ = watcher.Add(dir)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ":ok\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	for {
		select {
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			// index.lock and *.lock churn on every git invocation, even read-only ones.
			if strings.HasSuffix(ev.Name, ".lock") || ev.Op == fsnotify.Chmod {
				continue
			}
			// New branch namespaces (refs/heads/feat/...) need their own watch.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = watcher.Add(ev.Name)
				}
			}
			debounce.Reset(gitEventsDebounce)
		case <-debounce.C:
			if _, err := fmt.Fprint(w, "event: git_changed\ndata: {\"type\":\"git_changed\"}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ":heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-watcher.Errors:
		case <-r.Context().Done():
			return
		}
	}
}
