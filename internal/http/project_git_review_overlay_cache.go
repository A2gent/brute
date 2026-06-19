package http

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
)

func (s *Server) loadCachedProjectGitReviewOverlayAnnotations(projectID string, repoPath string, target projectGitBranchChangesTargetInfo, diffContext projectGitReviewOverlayDiffContext) ([]ProjectGitReviewOverlayAnnotation, error) {
	rows, err := s.store.ListProjectGitReviewOverlayCache(projectID, repoPath, target.CurrentBranch, target.BaseBranch)
	if err != nil {
		return nil, err
	}
	annotations := []ProjectGitReviewOverlayAnnotation{}
	for _, row := range rows {
		filePath := normalizeProjectGitReviewOverlayPath(row.FilePath)
		if filePath == "" || diffContext.DiffHashes[filePath] == "" || diffContext.DiffHashes[filePath] != strings.TrimSpace(row.DiffHash) {
			continue
		}
		lineIndex, ok := diffContext.AllowedLines[filePath]
		if !ok {
			continue
		}
		var cached []ProjectGitReviewOverlayAnnotation
		if err := json.Unmarshal([]byte(row.AnnotationsJSON), &cached); err != nil {
			logging.Warn("Skipping invalid review overlay cache for %s: %v", filePath, err)
			continue
		}
		for _, annotation := range cached {
			annotation.FilePath = normalizeProjectGitReviewOverlayPath(annotation.FilePath)
			annotation.Side = normalizeProjectGitReviewOverlaySide(annotation.Side)
			annotation.Title = truncateText(cleanProjectGitReviewOverlayText(annotation.Title), 90)
			annotation.Body = truncateText(cleanProjectGitReviewOverlayText(annotation.Body), 520)
			if annotation.FilePath != filePath || annotation.Side == "" || annotation.LineNumber <= 0 {
				continue
			}
			if !projectGitReviewOverlayLineAllowed(lineIndex, annotation.Side, annotation.LineNumber) {
				continue
			}
			if annotation.EndLineNumber <= 0 || annotation.EndLineNumber < annotation.LineNumber || !projectGitReviewOverlayLineAllowed(lineIndex, annotation.Side, annotation.EndLineNumber) {
				annotation.EndLineNumber = annotation.LineNumber
			}
			if !isUsefulProjectGitReviewOverlayAnnotation(annotation.Title, annotation.Body) {
				continue
			}
			annotations = append(annotations, annotation)
		}
	}
	sortProjectGitReviewOverlayAnnotations(annotations)
	return annotations, nil
}

func (s *Server) saveProjectGitReviewOverlayAnnotations(projectID string, repoPath string, target projectGitBranchChangesTargetInfo, diffHashes map[string]string, annotations []ProjectGitReviewOverlayAnnotation) error {
	byFile := map[string][]ProjectGitReviewOverlayAnnotation{}
	for _, annotation := range annotations {
		filePath := normalizeProjectGitReviewOverlayPath(annotation.FilePath)
		if filePath == "" || diffHashes[filePath] == "" {
			continue
		}
		annotation.FilePath = filePath
		byFile[filePath] = append(byFile[filePath], annotation)
	}
	now := time.Now()
	for filePath, diffHash := range diffHashes {
		fileAnnotations := byFile[filePath]
		if fileAnnotations == nil {
			fileAnnotations = []ProjectGitReviewOverlayAnnotation{}
		}
		payload, err := json.Marshal(fileAnnotations)
		if err != nil {
			return err
		}
		cache := &storage.ProjectGitReviewOverlayCache{
			ProjectID:       projectID,
			RepoPath:        repoPath,
			Branch:          target.CurrentBranch,
			BaseBranch:      target.BaseBranch,
			FilePath:        filePath,
			DiffHash:        diffHash,
			AnnotationsJSON: string(payload),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := s.store.SaveProjectGitReviewOverlayCache(cache); err != nil {
			return err
		}
	}
	return nil
}
