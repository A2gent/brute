package http

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const projectSearchMaxResults = 5

const projectSearchMaxFileResults = 20

func (s *Server) handleProjectSearch(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if query == "" {
		s.jsonResponse(w, http.StatusOK, ProjectSearchResponse{
			RootFolder:      resolvedRoot,
			Query:           "",
			FileNameMatches: []ProjectFileNameMatch{},
			ContentMatches:  []ProjectContentMatch{},
		})
		return
	}

	fileNameCandidates := make([]rankedFileNameMatch, 0, 32)
	contentCandidates := make([]rankedContentMatch, 0, 32)
	normalizedQueryLower := strings.ToLower(normalizeProjectSearchQuery(query))
	if normalizedQueryLower == "" {
		s.jsonResponse(w, http.StatusOK, ProjectSearchResponse{
			RootFolder:      resolvedRoot,
			Query:           query,
			FileNameMatches: []ProjectFileNameMatch{},
			ContentMatches:  []ProjectContentMatch{},
		})
		return
	}

	walkErr := filepath.WalkDir(resolvedRoot, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			if entry.IsDir() && fullPath != resolvedRoot {
				return filepath.SkipDir
			}
			if !entry.IsDir() {
				return nil
			}
		}

		if entry.IsDir() {
			return nil
		}

		relPath, relErr := filepath.Rel(resolvedRoot, fullPath)
		if relErr != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		if !isProjectEditableFile(relPath) {
			return nil
		}

		if score := scoreProjectFileNameMatch(relPath, normalizedQueryLower); score > 0 {
			fileNameCandidates = append(fileNameCandidates, rankedFileNameMatch{
				ProjectFileNameMatch: ProjectFileNameMatch{
					Path: relPath,
					Name: name,
				},
				score: score,
			})
		}

		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() > maxProjectEditableFileBytes {
			return nil
		}
		content, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			return nil
		}
		if err := validateProjectFileContent(content, "search"); err != nil {
			return nil
		}
		lineNumber, lineText, score := findProjectContentMatch(content, normalizedQueryLower)
		if score > 0 {
			contentCandidates = append(contentCandidates, rankedContentMatch{
				ProjectContentMatch: ProjectContentMatch{
					Path:    relPath,
					Line:    lineNumber,
					Preview: truncateText(strings.TrimSpace(lineText), 220),
				},
				score: score,
			})
		}

		return nil
	})
	if walkErr != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to search project files: "+walkErr.Error())
		return
	}

	sort.Slice(fileNameCandidates, func(i, j int) bool {
		if fileNameCandidates[i].score != fileNameCandidates[j].score {
			return fileNameCandidates[i].score > fileNameCandidates[j].score
		}
		return fileNameCandidates[i].Path < fileNameCandidates[j].Path
	})
	sort.Slice(contentCandidates, func(i, j int) bool {
		if contentCandidates[i].score != contentCandidates[j].score {
			return contentCandidates[i].score > contentCandidates[j].score
		}
		return contentCandidates[i].Path < contentCandidates[j].Path
	})

	fileNameMatches := make([]ProjectFileNameMatch, 0, projectSearchMaxFileResults)
	for _, candidate := range fileNameCandidates {
		fileNameMatches = append(fileNameMatches, candidate.ProjectFileNameMatch)
		if len(fileNameMatches) >= projectSearchMaxFileResults {
			break
		}
	}

	contentMatches := make([]ProjectContentMatch, 0, projectSearchMaxResults)
	for _, candidate := range contentCandidates {
		contentMatches = append(contentMatches, candidate.ProjectContentMatch)
		if len(contentMatches) >= projectSearchMaxResults {
			break
		}
	}

	s.jsonResponse(w, http.StatusOK, ProjectSearchResponse{
		RootFolder:      resolvedRoot,
		Query:           query,
		FileNameMatches: fileNameMatches,
		ContentMatches:  contentMatches,
	})
}

type rankedFileNameMatch struct {
	ProjectFileNameMatch
	score int
}

type rankedContentMatch struct {
	ProjectContentMatch
	score int
}

func normalizeProjectSearchQuery(query string) string {
	normalized := strings.TrimSpace(query)
	for len(normalized) >= 2 {
		first := normalized[0]
		last := normalized[len(normalized)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
			normalized = strings.TrimSpace(normalized[1 : len(normalized)-1])
			continue
		}
		break
	}
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	for strings.HasPrefix(normalized, "./") {
		normalized = strings.TrimPrefix(normalized, "./")
	}
	normalized = strings.TrimPrefix(normalized, "/")
	return normalized
}

func scoreProjectFileNameMatch(relPath string, queryLower string) int {
	baseName := strings.ToLower(filepath.Base(relPath))
	pathLower := strings.ToLower(relPath)

	score := 0
	switch {
	case baseName == queryLower:
		score = 120
	case strings.HasPrefix(baseName, queryLower):
		score = 100
	case strings.Contains(baseName, queryLower):
		score = 80
	case strings.HasPrefix(pathLower, queryLower):
		score = 70
	case strings.Contains(pathLower, queryLower):
		score = 50
	default:
		return 0
	}

	if pathLower == queryLower {
		score += 30
	}
	if len(relPath) <= 40 {
		score += 10
	}
	if len(relPath) <= 16 {
		score += 6
	}

	return score
}

func findProjectContentMatch(content []byte, queryLower string) (int, string, int) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lowerLine := strings.ToLower(line)
		matchIndex := strings.Index(lowerLine, queryLower)
		if matchIndex < 0 {
			continue
		}
		lineScore := 70 - index
		if lineScore < 12 {
			lineScore = 12
		}
		colScore := 20 - matchIndex
		if colScore < 0 {
			colScore = 0
		}
		return index + 1, line, lineScore + colScore
	}
	return 0, "", 0
}

func truncateText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}
