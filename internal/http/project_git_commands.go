package http

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type projectGitBranchChangesTargetInfo struct {
	CurrentBranch string
	BaseBranch    string
	BaseRef       string
	Available     bool
}

func projectGitBranchChangesAvailability(repoRoot string) (string, string, bool) {
	target := projectGitBranchChangesTarget(repoRoot)
	return target.CurrentBranch, target.BaseBranch, target.Available
}

func projectGitBranchChangesTarget(repoRoot string) projectGitBranchChangesTargetInfo {
	currentBranchOutput, err := runGitCommand(repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return projectGitBranchChangesTargetInfo{}
	}
	currentBranch := strings.TrimSpace(currentBranchOutput)
	if currentBranch == "HEAD" {
		currentBranch = inferProjectGitDetachedBranch(repoRoot)
	}
	if currentBranch == "" || currentBranch == "HEAD" || currentBranch == "master" || currentBranch == "main" {
		return projectGitBranchChangesTargetInfo{CurrentBranch: currentBranch}
	}

	for _, candidate := range []struct {
		label string
		ref   string
	}{
		{label: "master", ref: "refs/heads/master"},
		{label: "main", ref: "refs/heads/main"},
		{label: "origin/master", ref: "refs/remotes/origin/master"},
		{label: "origin/main", ref: "refs/remotes/origin/main"},
	} {
		if _, err := runGitCommand(repoRoot, "show-ref", "--verify", "--quiet", candidate.ref); err == nil {
			return projectGitBranchChangesTargetInfo{
				CurrentBranch: currentBranch,
				BaseBranch:    candidate.label,
				BaseRef:       candidate.ref,
				Available:     true,
			}
		}
	}
	return projectGitBranchChangesTargetInfo{CurrentBranch: currentBranch}
}

func inferProjectGitDetachedBranch(repoRoot string) string {
	output, err := runGitCommandPreserveLeading(
		repoRoot,
		"for-each-ref",
		"--points-at",
		"HEAD",
		"--format=%(refname:short)%1f%(refname)%1f%(committerdate:iso-strict)",
		"refs/heads",
	)
	if err != nil {
		return "HEAD"
	}

	type branchCandidate struct {
		name      string
		updatedAt string
	}
	candidates := make([]branchCandidate, 0)
	for _, line := range strings.Split(output, "\n") {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		parts := strings.Split(trimmedLine, "\x1f")
		name := strings.TrimSpace(parts[0])
		if name == "" || name == "master" || name == "main" {
			continue
		}
		updatedAt := ""
		if len(parts) >= 3 {
			updatedAt = strings.TrimSpace(parts[2])
		}
		candidates = append(candidates, branchCandidate{name: name, updatedAt: updatedAt})
	}
	if len(candidates) == 0 {
		return "HEAD"
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].updatedAt != candidates[j].updatedAt {
			return candidates[i].updatedAt > candidates[j].updatedAt
		}
		return candidates[i].name < candidates[j].name
	})
	return candidates[0].name
}

func projectHasGitMetadata(projectRoot string) bool {
	_, err := os.Stat(filepath.Join(projectRoot, ".git"))
	return err == nil
}

func resolveProjectGitTargetRoot(projectRoot string, repoPath string) (string, error) {
	trimmedPath := strings.TrimSpace(repoPath)
	if trimmedPath == "" {
		return projectRoot, nil
	}

	resolvedPath, _, err := resolveProjectPathAllowAbsolute(projectRoot, trimmedPath)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("target repo path does not exist")
		}
		return "", fmt.Errorf("failed to access target repo path: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("target repo path is not a directory")
	}

	return resolvedPath, nil
}

func runGitCommand(projectRoot string, args ...string) (string, error) {
	cmd := gitCommand(projectRoot, args...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			trimmed = err.Error()
		}
		return "", errors.New(trimmed)
	}
	return trimmed, nil
}

func runGitCommandPreserveLeading(projectRoot string, args ...string) (string, error) {
	cmd := gitCommand(projectRoot, args...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(output), "\r\n")
	if err != nil {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			trimmed = err.Error()
		}
		return "", errors.New(trimmed)
	}
	return text, nil
}

func runGitCommandWithExitCode(projectRoot string, args ...string) (string, int, error) {
	cmd := gitCommand(projectRoot, args...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimRight(string(output), "\r\n")
	if err == nil {
		return trimmed, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return trimmed, exitErr.ExitCode(), err
	}
	return trimmed, -1, err
}

// gitCommand runs git against projectRoot without inheriting ambient hook state.
// When tests (or tools) run under a parent `git commit`/`git push` hook, Git sets
// GIT_INDEX_FILE/GIT_DIR/etc; leaking those into nested repos breaks tree builds.
func gitCommand(projectRoot string, args ...string) *exec.Cmd {
	commandArgs := append([]string{"-C", projectRoot}, args...)
	cmd := exec.Command("git", commandArgs...)
	cmd.Env = scrubInheritedGitEnv(os.Environ())
	return cmd
}

func scrubInheritedGitEnv(env []string) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			cleaned = append(cleaned, entry)
			continue
		}
		switch key {
		case "GIT_DIR",
			"GIT_WORK_TREE",
			"GIT_INDEX_FILE",
			"GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_QUARANTINE_PATH",
			"GIT_PREFIX":
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

func resolveGitRepoFilePath(repoRoot string, relPath string) (string, error) {
	normalizedPath := filepath.Clean(strings.TrimSpace(relPath))
	if normalizedPath == "" || normalizedPath == "." {
		return "", errors.New("file path is required")
	}
	if filepath.IsAbs(normalizedPath) {
		return "", errors.New("file path must be relative")
	}

	resolvedPath := filepath.Clean(filepath.Join(repoRoot, normalizedPath))
	relToRoot, err := filepath.Rel(repoRoot, resolvedPath)
	if err != nil {
		return "", errors.New("invalid file path")
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return "", errors.New("file path escapes repository root")
	}

	return filepath.ToSlash(relToRoot), nil

}

func buildProjectGitBranches(repoRoot string, currentBranch string) ([]ProjectGitBranch, error) {
	output, err := runGitCommandPreserveLeading(
		repoRoot,
		"for-each-ref",
		"--format=%(refname:short)%1f%(HEAD)%1f%(upstream:track)%1f%(refname)%1f%(committerdate:iso-strict)",
		"refs/heads",
		"refs/remotes",
	)
	if err != nil {
		return nil, err
	}

	branches := make([]ProjectGitBranch, 0)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}

		parts := strings.Split(trimmedLine, "\x1f")
		if len(parts) < 4 {
			continue
		}

		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}

		refname := strings.TrimSpace(parts[3])
		remote := strings.HasPrefix(refname, "refs/remotes/")
		if remote && (strings.HasSuffix(refname, "/HEAD") || strings.HasSuffix(name, "/HEAD")) {
			continue
		}

		updatedAt := ""
		if len(parts) >= 5 {
			updatedAt = strings.TrimSpace(parts[4])
		}
		ahead, behind := parseGitAheadBehind(parts[2])
		current := strings.TrimSpace(parts[1]) == "*" || (!remote && name == currentBranch)

		branches = append(branches, ProjectGitBranch{
			Name:      name,
			Current:   current,
			Remote:    remote,
			Ahead:     ahead,
			Behind:    behind,
			UpdatedAt: updatedAt,
		})
	}

	sort.SliceStable(branches, func(i, j int) bool {
		left := branches[i]
		right := branches[j]
		if left.Current != right.Current {
			return left.Current
		}
		if left.Remote != right.Remote {
			return !left.Remote
		}
		return left.Name < right.Name
	})

	return branches, nil
}
