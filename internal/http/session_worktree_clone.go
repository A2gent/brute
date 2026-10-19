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
	"regexp"
	"runtime"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

// git names the folder that already has the branch checked out (wording differs by version).
var worktreeBranchInUsePattern = regexp.MustCompile(`(?:checked out|used by worktree) at '([^']+)'`)

// addSessionWorktree checks out branch at path. An existing local branch is continued as is,
// a remote-only one gets a local tracking branch; otherwise a new branch starts from baseRef.
func addSessionWorktree(root string, path string, branch string, baseRef string) error {
	args := []string{"-b", branch, path, baseRef}
	created := true
	if _, err := runGitCommand(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		args, created = []string{path, branch}, false
	} else if _, err := runGitCommand(root, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err == nil {
		args = []string{"--track", "-b", branch, path, "origin/" + branch}
	}
	if runtime.GOOS == "darwin" {
		args = append([]string{"--no-checkout"}, args...)
	}
	if _, err := runGitCommand(root, append([]string{"worktree", "add", "--relative-paths"}, args...)...); err != nil {
		// git refuses to check out one branch in two folders; tell the user which folder holds it.
		if match := worktreeBranchInUsePattern.FindStringSubmatch(err.Error()); match != nil {
			return fmt.Errorf("branch %s is open in %s; switch that folder to another branch first", branch, match[1])
		}
		return err
	}
	if runtime.GOOS != "darwin" {
		return nil
	}
	cloneProjectFolder(root, path)
	// reset fills the index from baseRef without touching files, checkout then rewrites only
	// files whose content differs from it, and clean drops untracked files cloned from the
	// main folder so they cannot end up in session commits. Ignored files stay cloned.
	for _, args := range [][]string{{"reset", "-q"}, {"checkout", "--", "."}, {"clean", "-fdq"}} {
		if _, err := runGitCommand(path, args...); err != nil {
			_, _ = runGitCommand(root, "worktree", "remove", "--force", path)
			// A continued branch existed before the session and must survive the failure.
			if created {
				_, _ = runGitCommand(root, "branch", "-D", branch)
			}
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
