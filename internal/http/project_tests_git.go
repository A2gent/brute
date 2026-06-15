package http

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var gitDiffHunkPattern = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func loadProjectGitTestingScopeChangedFiles(repoRoot string, target projectGitBranchChangesTargetInfo) ([]ProjectGitCommitFile, error) {
	merged := map[string]ProjectGitCommitFile{}
	if target.Available {
		files, err := loadProjectGitBranchChangedFiles(repoRoot, target)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			merged[file.Path] = file
		}
	}

	workingTreeFiles, err := loadProjectGitWorkingTreeChangedFiles(repoRoot)
	if err != nil {
		return nil, err
	}
	for _, file := range workingTreeFiles {
		if existing, ok := merged[file.Path]; ok {
			merged[file.Path] = mergeProjectGitTestingScopeFile(existing, file)
			continue
		}
		merged[file.Path] = file
	}

	files := make([]ProjectGitCommitFile, 0, len(merged))
	for _, file := range merged {
		files = append(files, file)
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func loadProjectGitWorkingTreeChangedFiles(repoRoot string) ([]ProjectGitCommitFile, error) {
	statusOutput, err := runGitCommandPreserveLeading(repoRoot, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	statuses := map[string]string{}
	for _, file := range parseGitPorcelain(statusOutput) {
		status := projectGitTestingScopeStatusFromPorcelain(file)
		if status == "" {
			continue
		}
		statuses[file.Path] = status
	}
	if len(statuses) == 0 {
		return []ProjectGitCommitFile{}, nil
	}

	statsParts := make([]string, 0, 2)
	if statsOutput, err := runGitCommandPreserveLeading(repoRoot, "diff", "--numstat", "--find-renames"); err == nil && strings.TrimSpace(statsOutput) != "" {
		statsParts = append(statsParts, statsOutput)
	}
	if statsOutput, err := runGitCommandPreserveLeading(repoRoot, "diff", "--cached", "--numstat", "--find-renames"); err == nil && strings.TrimSpace(statsOutput) != "" {
		statsParts = append(statsParts, statsOutput)
	}
	return mergeProjectGitCommitFiles(statuses, strings.Join(statsParts, "\n")), nil
}

func mergeProjectGitTestingScopeFile(existing ProjectGitCommitFile, dirty ProjectGitCommitFile) ProjectGitCommitFile {
	merged := existing
	if dirty.Status == "D" {
		merged.Status = "D"
	} else if existing.Status != "A" {
		merged.Status = dirty.Status
	}
	merged.Additions += dirty.Additions
	merged.Deletions += dirty.Deletions
	merged.Binary = merged.Binary || dirty.Binary
	return merged
}

func projectGitTestingScopeStatusFromPorcelain(file ProjectGitChangedFile) string {
	if file.Untracked {
		return "A"
	}
	indexStatus := strings.TrimSpace(file.IndexStatus)
	worktreeStatus := strings.TrimSpace(file.WorktreeStatus)
	if indexStatus == "D" || worktreeStatus == "D" {
		return "D"
	}
	if indexStatus == "R" || worktreeStatus == "R" {
		return "R"
	}
	if indexStatus == "A" || worktreeStatus == "A" {
		return "A"
	}
	if indexStatus != "" || worktreeStatus != "" {
		return "M"
	}
	return ""
}

func projectTestsBranchScopeHash(discovery ProjectTestsDiscoveryResponse) string {
	hash := sha256.New()
	writeProjectTestsScopeHashPart(hash, "branch", discovery.CurrentBranch)
	writeProjectTestsScopeHashPart(hash, "base", discovery.BaseBranch)
	writeProjectTestsScopeHashPart(hash, "repo", discovery.RepoPath)
	if payload, err := json.Marshal(discovery.BranchTestFiles); err == nil {
		writeProjectTestsScopeHashPart(hash, "branch-tests", string(payload))
	}
	if payload, err := json.Marshal(changedCodeFileSet(discovery)); err == nil {
		writeProjectTestsScopeHashPart(hash, "changed-code", string(payload))
	}
	if projectHasGitMetadata(discovery.RootFolder) {
		writeProjectTestsWorkingTreeScopeHash(hash, discovery.RootFolder)
		target := projectGitBranchChangesTarget(discovery.RootFolder)
		if target.Available {
			if diff, err := runGitCommandPreserveLeading(discovery.RootFolder, "diff", "--no-color", "--find-renames", target.BaseRef+"...HEAD"); err == nil {
				writeProjectTestsScopeHashPart(hash, "diff", diff)
			}
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func writeProjectTestsWorkingTreeScopeHash(hash hashWriter, repoRoot string) {
	statusOutput, err := runGitCommandPreserveLeading(repoRoot, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return
	}
	writeProjectTestsScopeHashPart(hash, "worktree-status", statusOutput)
	if diff, err := runGitCommandPreserveLeading(repoRoot, "diff", "--no-color", "--find-renames"); err == nil {
		writeProjectTestsScopeHashPart(hash, "worktree-diff", diff)
	}
	if diff, err := runGitCommandPreserveLeading(repoRoot, "diff", "--cached", "--no-color", "--find-renames"); err == nil {
		writeProjectTestsScopeHashPart(hash, "index-diff", diff)
	}
	for _, file := range parseGitPorcelain(statusOutput) {
		if !file.Untracked {
			continue
		}
		fullPath := filepath.Join(repoRoot, filepath.FromSlash(file.Path))
		info, err := os.Stat(fullPath)
		if err != nil {
			writeProjectTestsScopeHashPart(hash, "untracked-file", file.Path)
			continue
		}
		if info.IsDir() || info.Size() > 2*1024*1024 {
			writeProjectTestsScopeHashPart(hash, "untracked-file", fmt.Sprintf("%s:%d:%d", file.Path, info.Size(), info.ModTime().UnixNano()))
			continue
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		writeProjectTestsScopeHashPart(hash, "untracked-file", file.Path)
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
}

func writeProjectTestsScopeHashPart(hash hashWriter, label string, value string) {
	_, _ = hash.Write([]byte(label))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func projectGitTestingScopeAddedLineNumbers(repoRoot string, target projectGitBranchChangesTargetInfo, status string, relPath string) map[int]bool {
	lines := map[int]bool{}
	if status == "A" {
		lines[-1] = true
		return lines
	}

	baseRef := ""
	if target.Available {
		baseRef = target.BaseRef
	} else if _, err := runGitCommand(repoRoot, "rev-parse", "--verify", "HEAD"); err == nil {
		baseRef = "HEAD"
	}
	if baseRef == "" {
		return lines
	}

	diff, err := runGitCommandPreserveLeading(repoRoot, "diff", "--unified=0", "--no-color", "--find-renames", baseRef, "--", relPath)
	if err != nil {
		return lines
	}
	currentLine := 0
	for _, line := range strings.Split(diff, "\n") {
		if matches := gitDiffHunkPattern.FindStringSubmatch(line); matches != nil {
			currentLine, _ = strconv.Atoi(matches[1])
			continue
		}
		if currentLine <= 0 {
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			lines[currentLine] = true
			currentLine++
			continue
		}
		if strings.HasPrefix(line, "-") {
			continue
		}
		currentLine++
	}
	return lines
}
