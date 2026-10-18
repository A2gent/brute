// session_worktree_clone.go fills new session worktrees with APFS clones of the main
// project folder on macOS. A plain checkout writes every file again (gigabytes for
// asset-heavy projects) and starts without ignored caches like .godot or node_modules;
// clones share disk blocks with the main folder until a file is edited.
package http

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

// addSessionWorktree creates the worktree for a new branch at path from baseRef.
func addSessionWorktree(root string, path string, branch string, baseRef string) error {
	if runtime.GOOS != "darwin" {
		_, err := runGitCommand(root, "worktree", "add", "--relative-paths", "-b", branch, path, baseRef)
		return err
	}
	if _, err := runGitCommand(root, "worktree", "add", "--relative-paths", "--no-checkout", "-b", branch, path, baseRef); err != nil {
		return err
	}
	cloneProjectFolder(root, path)
	// reset fills the index from baseRef without touching files, checkout then rewrites only
	// files whose content differs from it, and clean drops untracked files cloned from the
	// main folder so they cannot end up in session commits. Ignored files stay cloned.
	for _, args := range [][]string{{"reset", "-q"}, {"checkout", "--", "."}, {"clean", "-fdq"}} {
		if _, err := runGitCommand(path, args...); err != nil {
			_, _ = runGitCommand(root, "worktree", "remove", "--force", path)
			_, _ = runGitCommand(root, "branch", "-D", branch)
			return fmt.Errorf("failed to populate session worktree: %w", err)
		}
	}
	return nil
}

// cloneProjectFolder is best effort: files cp cannot clone (another volume, sockets, no
// permission) are written by git checkout if tracked and skipped otherwise.
func cloneProjectFolder(root string, target string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		logging.Warn("Session worktree clone skipped: path=%s error=%v", target, err)
		return
	}
	args := []string{"-cRp"}
	for _, entry := range entries {
		source := filepath.Join(root, entry.Name())
		// The worktrees folder may live inside the project, cloning it would recurse.
		if entry.Name() == ".git" || pathWithin(target, source) {
			continue
		}
		args = append(args, source)
	}
	if len(args) == 1 {
		return
	}
	args = append(args, target+string(filepath.Separator))
	if output, err := exec.Command("cp", args...).CombinedOutput(); err != nil {
		logging.Warn("Session worktree clone incomplete: path=%s error=%v output=%s", target, err, strings.TrimSpace(string(output)))
	}
}

func pathWithin(path string, dir string) bool {
	resolve := func(value string) string {
		if resolved, err := filepath.EvalSymlinks(value); err == nil {
			return resolved
		}
		return filepath.Clean(value)
	}
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
