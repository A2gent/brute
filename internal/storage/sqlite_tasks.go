package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrTaskNotFound = errors.New("task not found")

func (s *SQLiteStore) CreateTask(projectID string, input TaskCreate) (*Task, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, fmt.Errorf("task title is required")
	}
	status := normalizeTaskStatus(input.Status)
	if status == "" {
		return nil, fmt.Errorf("invalid task status %q", input.Status)
	}
	if input.Priority < 0 || input.Priority > 4 {
		return nil, fmt.Errorf("priority must be between 0 and 4")
	}
	if input.Complexity < 0 || input.Complexity > 5 {
		return nil, fmt.Errorf("complexity must be between 0 and 5")
	}
	project, err := s.GetProject(projectID)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if sourceKey := strings.TrimSpace(input.SourceKey); sourceKey != "" {
		existing, err := getTaskBySourceKeyTx(tx, projectID, sourceKey)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, ErrTaskNotFound) {
			return nil, err
		}
	}

	var seq int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM tasks WHERE project_id = ?`, projectID).Scan(&seq); err != nil {
		return nil, fmt.Errorf("allocate task ref: %w", err)
	}
	position := float64(seq)
	if input.Position != nil {
		position = *input.Position
	}
	createdBy := strings.TrimSpace(input.CreatedBy)
	if createdBy == "" {
		createdBy = "user"
	}
	now := time.Now().UTC()
	task := &Task{
		ID: uuid.New().String(), ProjectID: projectID, Ref: taskRefPrefix(project) + fmt.Sprintf("-%d", seq), Seq: seq,
		Title: title, Body: input.Body, Image: input.Image, Status: status, Priority: input.Priority, Complexity: input.Complexity,
		Tags: normalizeTaskTags(input.Tags), Price: strings.TrimSpace(input.Price), Position: position,
		CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now, SourceKey: strings.TrimSpace(input.SourceKey),
	}
	stampTaskStatus(task, "", status, now)
	imageJSON := []byte{}
	if task.Image != nil {
		imageJSON, _ = json.Marshal(task.Image)
	}
	tagsJSON, _ := json.Marshal(task.Tags)
	_, err = tx.Exec(`INSERT INTO tasks
		(id, project_id, ref, seq, title, body, image, session_id, status, priority, complexity, tags, price, position, created_by, source_key, created_at, updated_at, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.ProjectID, task.Ref, task.Seq, task.Title, task.Body, string(imageJSON), task.SessionID, task.Status, task.Priority, task.Complexity,
		string(tagsJSON), task.Price, task.Position, task.CreatedBy, task.SourceKey, task.CreatedAt, task.UpdatedAt,
		task.StartedAt, task.CompletedAt)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit task: %w", err)
	}
	return task, nil
}

func (s *SQLiteStore) ListTasks(projectID string) ([]*Task, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	rows, err := s.db.Query(taskSelect+` WHERE project_id = ? ORDER BY status, position, seq`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	tasks := []*Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *SQLiteStore) GetTask(projectID, taskRef string) (*Task, error) {
	projectID, taskRef = strings.TrimSpace(projectID), strings.TrimSpace(taskRef)
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if taskRef == "" {
		return nil, fmt.Errorf("task id is required")
	}
	return scanTask(s.db.QueryRow(taskSelect+` WHERE project_id = ? AND (id = ? OR ref = ?)`, projectID, taskRef, taskRef))
}

func (s *SQLiteStore) UpdateTask(projectID, taskRef string, update TaskUpdate) (*Task, error) {
	task, err := s.GetTask(projectID, taskRef)
	if err != nil {
		return nil, err
	}
	previousStatus := task.Status
	if update.Title != nil {
		task.Title = strings.TrimSpace(*update.Title)
		if task.Title == "" {
			return nil, fmt.Errorf("task title is required")
		}
	}
	if update.Body != nil {
		task.Body = *update.Body
	}
	if update.Image != nil {
		task.Image = *update.Image
	}
	if update.SessionID != nil {
		task.SessionID = strings.TrimSpace(*update.SessionID)
	}
	if update.Status != nil {
		task.Status = normalizeTaskStatus(*update.Status)
		if task.Status == "" {
			return nil, fmt.Errorf("invalid task status %q", *update.Status)
		}
	}
	if update.Priority != nil {
		if *update.Priority < 0 || *update.Priority > 4 {
			return nil, fmt.Errorf("priority must be between 0 and 4")
		}
		task.Priority = *update.Priority
	}
	if update.Complexity != nil {
		if *update.Complexity < 0 || *update.Complexity > 5 {
			return nil, fmt.Errorf("complexity must be between 0 and 5")
		}
		task.Complexity = *update.Complexity
	}
	if update.Tags != nil {
		task.Tags = normalizeTaskTags(*update.Tags)
	}
	if update.Price != nil {
		task.Price = strings.TrimSpace(*update.Price)
	}
	if update.Position != nil {
		task.Position = *update.Position
	}
	task.UpdatedAt = time.Now().UTC()
	stampTaskStatus(task, previousStatus, task.Status, task.UpdatedAt)
	tagsJSON, _ := json.Marshal(task.Tags)
	imageJSON := []byte{}
	if task.Image != nil {
		imageJSON, _ = json.Marshal(task.Image)
	}
	result, err := s.db.Exec(`UPDATE tasks SET title=?, body=?, image=?, session_id=?, status=?, priority=?, complexity=?, tags=?, price=?, position=?, updated_at=?, started_at=?, completed_at=? WHERE project_id=? AND id=?`,
		task.Title, task.Body, string(imageJSON), task.SessionID, task.Status, task.Priority, task.Complexity, string(tagsJSON), task.Price, task.Position,
		task.UpdatedAt, task.StartedAt, task.CompletedAt, projectID, task.ID)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrTaskNotFound
	}
	return task, nil
}

func (s *SQLiteStore) DeleteTask(projectID, taskRef string) error {
	task, err := s.GetTask(projectID, taskRef)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`DELETE FROM tasks WHERE project_id = ? AND id = ?`, projectID, task.ID)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrTaskNotFound
	}
	return nil
}

const taskSelect = `SELECT id, project_id, ref, seq, title, body, image, session_id, status, priority, complexity, tags, price, position, created_by, source_key, created_at, updated_at, started_at, completed_at FROM tasks`

type taskScanner interface{ Scan(dest ...any) error }

func scanTask(scanner taskScanner) (*Task, error) {
	var task Task
	var tagsJSON, imageJSON string
	var startedAt, completedAt sql.NullTime
	if err := scanner.Scan(&task.ID, &task.ProjectID, &task.Ref, &task.Seq, &task.Title, &task.Body, &imageJSON, &task.SessionID, &task.Status,
		&task.Priority, &task.Complexity, &tagsJSON, &task.Price, &task.Position, &task.CreatedBy, &task.SourceKey,
		&task.CreatedAt, &task.UpdatedAt, &startedAt, &completedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("scan task: %w", err)
	}
	_ = json.Unmarshal([]byte(tagsJSON), &task.Tags)
	if strings.TrimSpace(imageJSON) != "" {
		_ = json.Unmarshal([]byte(imageJSON), &task.Image)
	}
	if task.Tags == nil {
		task.Tags = []string{}
	}
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}
	return &task, nil
}

func getTaskBySourceKeyTx(tx *sql.Tx, projectID, sourceKey string) (*Task, error) {
	return scanTask(tx.QueryRow(taskSelect+` WHERE project_id = ? AND source_key = ?`, projectID, sourceKey))
}

func normalizeTaskStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return TaskStatusTodo
	}
	if _, ok := taskStatuses[status]; !ok {
		return ""
	}
	return status
}

func normalizeTaskTags(tags []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(tag, "#")))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	return result
}

var nonRefCharacter = regexp.MustCompile(`[^A-Z0-9]+`)

func taskRefPrefix(project *Project) string {
	if project != nil {
		if configured := strings.ToUpper(strings.TrimSpace(project.Settings["task_ref_prefix"])); configured != "" {
			if normalized := strings.Trim(nonRefCharacter.ReplaceAllString(configured, ""), "-"); normalized != "" {
				return normalized
			}
		}
		parts := strings.Fields(project.Name)
		var initials strings.Builder
		for _, part := range parts {
			initials.WriteByte(strings.ToUpper(part)[0])
		}
		if initials.Len() > 0 {
			return initials.String()
		}
	}
	return "T"
}

func stampTaskStatus(task *Task, previous, next string, now time.Time) {
	if next == TaskStatusInProgress && previous != TaskStatusInProgress && task.StartedAt == nil {
		task.StartedAt = &now
	}
	if next == TaskStatusDone || next == TaskStatusCancelled {
		task.CompletedAt = &now
	} else if previous == TaskStatusDone || previous == TaskStatusCancelled {
		task.CompletedAt = nil
	}
}
