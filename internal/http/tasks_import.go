package http

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/A2gent/brute/internal/storage"
)

var (
	markdownTaskPattern    = regexp.MustCompile(`^(\s*)-\s+\[( |x|X)\]\s+(.*?)(?:\s+<!--\s*task-file:\s*([^\s][^>]*)\s*-->)?\s*$`)
	markdownHeadingPattern = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)
	markdownTagPattern     = regexp.MustCompile(`(?:^|\s)#([[:alnum:]_-]+)`)
	markdownPricePattern   = regexp.MustCompile(`(?i)(?:[$€]\s*[0-9][0-9,. ]*|[0-9][0-9,. ]*\s*(?:[$€]|USD\b|EUR\b)|\b(?:USD|EUR)\s+[0-9][0-9,. ]*)`)
)

type markdownTask struct {
	LineIndex int
	RawLine   string
	Title     string
	Status    string
	Tags      []string
	Price     string
	Body      string
	SourceKey string
}

type taskImportResult struct {
	Imported      *storage.Task `json:"imported,omitempty"`
	Remaining     int           `json:"remaining"`
	SourcePath    string        `json:"source_path"`
	SourceDeleted bool          `json:"source_deleted"`
}

// importNextMarkdownTask deliberately migrates one source line at a time. The task row is
// committed first; only then is its exact source line removed, making interrupted imports resumable.
func (s *Server) importNextMarkdownTask(projectID, sourcePath string) (*taskImportResult, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		return nil, fmt.Errorf("project folder is not configured")
	}
	if strings.TrimSpace(sourcePath) == "" {
		sourcePath = "TODO.md"
	}
	resolvedRoot, err := filepath.Abs(strings.TrimSpace(*project.Folder))
	if err != nil {
		return nil, fmt.Errorf("resolve project folder: %w", err)
	}
	resolvedPath, normalizedPath, err := resolveProjectPath(resolvedRoot, sourcePath)
	if err != nil {
		return nil, err
	}
	if normalizedPath == "" || !isTodoSourcePath(normalizedPath) {
		return nil, fmt.Errorf("source must be TODO.md or TO-DO.md")
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("read task source: %w", err)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("stat task source: %w", err)
	}
	tasks := parseMarkdownTasks(string(content), resolvedRoot)
	result := &taskImportResult{Remaining: len(tasks), SourcePath: normalizedPath}
	remover := s.removeTaskSource
	if remover == nil {
		remover = os.Remove
	}
	if len(tasks) == 0 {
		if err := remover(resolvedPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("delete empty task source: %w", err)
		}
		result.SourceDeleted = true
		return result, nil
	}

	next := tasks[0]
	created, err := s.store.CreateTask(projectID, storage.TaskCreate{
		Title: next.Title, Body: next.Body, Status: next.Status, Priority: 2, Tags: next.Tags, Price: next.Price,
		Position: float64Ptr(float64(next.LineIndex + 1)), CreatedBy: "user", SourceKey: next.SourceKey,
	})
	if err != nil {
		return nil, err
	}
	verified, err := s.store.GetTask(projectID, created.ID)
	if err != nil || verified.SourceKey != next.SourceKey {
		return nil, fmt.Errorf("verify imported task: %w", err)
	}

	updated := removeMarkdownLine(string(content), next.LineIndex)
	remaining := parseMarkdownTasks(updated, resolvedRoot)
	result.Imported = created
	result.Remaining = len(remaining)
	if len(remaining) == 0 {
		if err := remover(resolvedPath); err != nil {
			return nil, fmt.Errorf("remove imported task source: %w", err)
		}
		result.SourceDeleted = true
		return result, nil
	}
	writer := s.writeTaskSource
	if writer == nil {
		writer = os.WriteFile
	}
	if err := writer(resolvedPath, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("remove imported task line: %w", err)
	}
	return result, nil
}

func parseMarkdownTasks(content, root string) []markdownTask {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	status := storage.TaskStatusTodo
	tasks := []markdownTask{}
	for index, line := range lines {
		if heading := markdownHeadingPattern.FindStringSubmatch(strings.TrimSpace(line)); heading != nil {
			status = markdownHeadingStatus(heading[1])
			continue
		}
		match := markdownTaskPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		title := strings.TrimSpace(match[3])
		taskStatus := status
		if strings.EqualFold(match[2], "x") {
			taskStatus = storage.TaskStatusDone
		}
		body := ""
		if linked := strings.TrimSpace(match[4]); linked != "" {
			if path, _, err := resolveProjectPath(root, linked); err == nil {
				if detail, err := os.ReadFile(path); err == nil {
					body = string(detail)
				}
			}
		}
		hash := sha256.Sum256([]byte(line))
		tasks = append(tasks, markdownTask{
			LineIndex: index, RawLine: line, Title: title, Status: taskStatus,
			Tags: extractMarkdownTags(title), Price: extractMarkdownPrice(title), Body: body,
			SourceKey: "markdown:" + hex.EncodeToString(hash[:]),
		})
	}
	return tasks
}

func markdownHeadingStatus(heading string) string {
	normalized := strings.ToLower(strings.TrimSpace(heading))
	normalized = strings.ReplaceAll(normalized, "-", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")
	switch normalized {
	case "ideas", "idea", "backlog":
		return storage.TaskStatusIdea
	case "in progress", "doing":
		return storage.TaskStatusInProgress
	case "review", "in review":
		return storage.TaskStatusInReview
	case "testing", "test":
		return storage.TaskStatusTesting
	case "done", "completed":
		return storage.TaskStatusDone
	case "cancelled", "canceled":
		return storage.TaskStatusCancelled
	default:
		return storage.TaskStatusTodo
	}
}

func extractMarkdownTags(title string) []string {
	matches := markdownTagPattern.FindAllStringSubmatch(title, -1)
	tags := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		tag := strings.ToLower(strings.TrimSpace(match[1]))
		if _, ok := seen[tag]; tag == "" || ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func extractMarkdownPrice(title string) string {
	return strings.Join(strings.Fields(markdownPricePattern.FindString(title)), " ")
}

func removeMarkdownLine(content string, index int) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if index < 0 || index >= len(lines) {
		return content
	}
	lines = append(lines[:index], lines[index+1:]...)
	return strings.Join(lines, "\n")
}

func isTodoSourcePath(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return name == "todo.md" || name == "to-do.md"
}

func float64Ptr(value float64) *float64 { return &value }
