// tasks_tool.go exposes the project task board (SQLite, previously TODO.md) to agents.
// Output is deliberately compact text instead of JSON dumps: an agent should be able to poll the
// board without spending the context it used to spend on reading a whole TODO.md.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type tasksTool struct {
	server *Server
}

type tasksToolParams struct {
	Action     string    `json:"action"`
	Ref        string    `json:"ref,omitempty"`
	Title      string    `json:"title,omitempty"`
	Body       *string   `json:"body,omitempty"`
	Status     string    `json:"status,omitempty"`
	Priority   *int      `json:"priority,omitempty"`
	Complexity *int      `json:"complexity,omitempty"`
	Tags       *[]string `json:"tags,omitempty"`
	Price      *string   `json:"price,omitempty"`
	Query      string    `json:"q,omitempty"`
	Tag        string    `json:"tag,omitempty"`
	Limit      int       `json:"limit,omitempty"`
	Claim      bool      `json:"claim,omitempty"`
	ProjectID  string    `json:"project_id,omitempty"`
}

func newTasksTool(server *Server) *tasksTool {
	return &tasksTool{server: server}
}

func (t *tasksTool) Name() string { return "tasks" }

func (t *tasksTool) Description() string {
	return `Read and manage the project task board (the database that replaced TODO.md). Actions: list (filter by status/tag/text), next (pick the next open task, optionally claim it), get (full task with body), create, update, delete, stats (counts per status). Tasks are scoped to the current session's project and identified by a short ref like AG-42. Use this instead of reading or writing TODO.md.`
}

func (t *tasksTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"list", "next", "get", "create", "update", "delete", "stats"},
				"description": "Operation to perform. Defaults to list.",
			},
			"ref": map[string]interface{}{
				"type":        "string",
				"description": "Task ref (AG-42) or UUID. Required for get, update and delete.",
			},
			"title":  map[string]interface{}{"type": "string", "description": "Task title. Required for create."},
			"body":   map[string]interface{}{"type": "string", "description": "Markdown detail of the task."},
			"status": map[string]interface{}{"type": "string", "description": "One of idea, todo, in_progress, in_review, testing, done, cancelled. For list it accepts a comma-separated filter."},
			"priority": map[string]interface{}{
				"type": "integer", "description": "0 highest .. 4 lowest (default 2).",
			},
			"complexity": map[string]interface{}{
				"type": "integer", "description": "0 unknown, 1 trivial .. 5 hard.",
			},
			"tags":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Tags without '#'."},
			"price": map[string]interface{}{"type": "string", "description": "Free-text price/estimate, as used on the board."},
			"q":     map[string]interface{}{"type": "string", "description": "Free-text filter over title and body (list only)."},
			"tag":   map[string]interface{}{"type": "string", "description": "Filter by a single tag (list only)."},
			"limit": map[string]interface{}{"type": "integer", "description": "Max rows for list (default 20)."},
			"claim": map[string]interface{}{"type": "boolean", "description": "For next: immediately move the picked task to in_progress."},
			"project_id": map[string]interface{}{
				"type": "string", "description": "Only needed when the session has no project bound.",
			},
		},
	}
}

func (t *tasksTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p tasksToolParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}
	projectID, err := t.resolveProjectID(ctx, p.ProjectID)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		action = "list"
	}
	var output string
	switch action {
	case "list":
		output, err = t.list(projectID, p)
	case "next":
		output, err = t.next(projectID, p)
	case "get":
		output, err = t.get(projectID, p)
	case "create":
		output, err = t.create(projectID, p)
	case "update":
		output, err = t.update(projectID, p)
	case "delete":
		output, err = t.delete(projectID, p)
	case "stats":
		output, err = t.stats(projectID)
	default:
		err = fmt.Errorf("unknown action %q", action)
	}
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	return &tools.Result{Success: true, Output: output, Metadata: map[string]interface{}{
		"action": action, "project_id": projectID,
	}}, nil
}

// resolveProjectID keeps the board scoped to the calling session's project so an agent cannot read
// another project's tasks; an explicit project_id is only honoured for project-less sessions.
func (t *tasksTool) resolveProjectID(ctx context.Context, requested string) (string, error) {
	sessionProjectID := ""
	if t.server != nil && t.server.sessionManager != nil {
		if sessionID, _ := ctx.Value("session_id").(string); strings.TrimSpace(sessionID) != "" {
			if sess, err := t.server.sessionManager.Get(sessionID); err == nil && sess != nil && sess.ProjectID != nil {
				sessionProjectID = strings.TrimSpace(*sess.ProjectID)
			}
		}
	}
	if sessionProjectID != "" {
		return sessionProjectID, nil
	}
	if requested = strings.TrimSpace(requested); requested != "" {
		return requested, nil
	}
	return "", fmt.Errorf("this session has no project bound: open the session from a project or pass project_id")
}

func (t *tasksTool) list(projectID string, p tasksToolParams) (string, error) {
	tasks, err := t.server.store.ListTasks(projectID)
	if err != nil {
		return "", err
	}
	total := len(tasks)
	filtered := filterTasks(tasks, p)
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}
	shown := filtered
	if len(shown) > limit {
		shown = shown[:limit]
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d task(s) match, showing %d (project total %d)\n", len(filtered), len(shown), total)
	for _, task := range shown {
		out.WriteString(formatTaskLine(task) + "\n")
	}
	if len(shown) == 0 {
		out.WriteString("(no tasks match this filter)\n")
	}
	out.WriteString(formatTaskCounts(tasks))
	return out.String(), nil
}

func (t *tasksTool) next(projectID string, p tasksToolParams) (string, error) {
	tasks, err := t.server.store.ListTasks(projectID)
	if err != nil {
		return "", err
	}
	statuses := parseStatusFilter(p.Status)
	if len(statuses) == 0 {
		statuses = map[string]struct{}{storage.TaskStatusTodo: {}, storage.TaskStatusIdea: {}}
	}
	candidates := []*storage.Task{}
	for _, task := range tasks {
		if _, ok := statuses[task.Status]; ok && matchesTaskText(task, p) {
			candidates = append(candidates, task)
		}
	}
	if len(candidates) == 0 {
		return "No open task available.\n" + formatTaskCounts(tasks), nil
	}
	// todo before idea, then priority, then the cheapest task, then board order.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if (a.Status == storage.TaskStatusTodo) != (b.Status == storage.TaskStatusTodo) {
			return a.Status == storage.TaskStatusTodo
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Complexity != b.Complexity {
			return a.Complexity < b.Complexity
		}
		return a.Position < b.Position
	})
	task := candidates[0]
	if p.Claim {
		inProgress := storage.TaskStatusInProgress
		if task, err = t.server.store.UpdateTask(projectID, task.ID, storage.TaskUpdate{Status: &inProgress}); err != nil {
			return "", err
		}
	}
	hint := "\nClaim it with action=update ref=" + task.Ref + " status=in_progress."
	if p.Claim {
		hint = "\nClaimed. Report progress with action=update ref=" + task.Ref + " status=in_review|testing|done."
	}
	return formatTaskDetail(task) + hint, nil
}

func (t *tasksTool) get(projectID string, p tasksToolParams) (string, error) {
	if strings.TrimSpace(p.Ref) == "" {
		return "", fmt.Errorf("ref is required for get")
	}
	task, err := t.server.store.GetTask(projectID, p.Ref)
	if err != nil {
		return "", err
	}
	return formatTaskDetail(task), nil
}

func (t *tasksTool) create(projectID string, p tasksToolParams) (string, error) {
	if strings.TrimSpace(p.Title) == "" {
		return "", fmt.Errorf("title is required for create")
	}
	priority := 2
	if p.Priority != nil {
		priority = *p.Priority
	}
	create := storage.TaskCreate{
		Title: p.Title, Status: p.Status, Priority: priority, CreatedBy: "agent",
	}
	if p.Body != nil {
		create.Body = *p.Body
	}
	if p.Complexity != nil {
		create.Complexity = *p.Complexity
	}
	if p.Tags != nil {
		create.Tags = *p.Tags
	}
	if p.Price != nil {
		create.Price = *p.Price
	}
	task, err := t.server.store.CreateTask(projectID, create)
	if err != nil {
		return "", err
	}
	return "Created " + formatTaskLine(task), nil
}

func (t *tasksTool) update(projectID string, p tasksToolParams) (string, error) {
	if strings.TrimSpace(p.Ref) == "" {
		return "", fmt.Errorf("ref is required for update")
	}
	update := storage.TaskUpdate{Body: p.Body, Priority: p.Priority, Complexity: p.Complexity, Tags: p.Tags, Price: p.Price}
	if strings.TrimSpace(p.Title) != "" {
		update.Title = &p.Title
	}
	if strings.TrimSpace(p.Status) != "" {
		update.Status = &p.Status
	}
	task, err := t.server.store.UpdateTask(projectID, p.Ref, update)
	if err != nil {
		return "", err
	}
	return "Updated " + formatTaskLine(task), nil
}

func (t *tasksTool) delete(projectID string, p tasksToolParams) (string, error) {
	if strings.TrimSpace(p.Ref) == "" {
		return "", fmt.Errorf("ref is required for delete")
	}
	if err := t.server.store.DeleteTask(projectID, p.Ref); err != nil {
		return "", err
	}
	return "Deleted " + strings.TrimSpace(p.Ref), nil
}

func (t *tasksTool) stats(projectID string) (string, error) {
	tasks, err := t.server.store.ListTasks(projectID)
	if err != nil {
		return "", err
	}
	return formatTaskCounts(tasks), nil
}

func filterTasks(tasks []*storage.Task, p tasksToolParams) []*storage.Task {
	statuses := parseStatusFilter(p.Status)
	tag := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p.Tag), "#"))
	filtered := []*storage.Task{}
	for _, task := range tasks {
		if len(statuses) > 0 {
			if _, ok := statuses[task.Status]; !ok {
				continue
			}
		}
		if tag != "" && !containsString(task.Tags, tag) {
			continue
		}
		if !matchesTaskText(task, p) {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func matchesTaskText(task *storage.Task, p tasksToolParams) bool {
	query := strings.ToLower(strings.TrimSpace(p.Query))
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(task.Title), query) || strings.Contains(strings.ToLower(task.Body), query)
}

func parseStatusFilter(raw string) map[string]struct{} {
	statuses := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			statuses[part] = struct{}{}
		}
	}
	return statuses
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func formatTaskLine(task *storage.Task) string {
	line := fmt.Sprintf("%s [%s p%d", task.Ref, task.Status, task.Priority)
	if task.Complexity > 0 {
		line += fmt.Sprintf(" c%d", task.Complexity)
	}
	line += "] " + task.Title
	for _, tag := range task.Tags {
		line += " #" + tag
	}
	if task.Price != "" {
		line += " (" + task.Price + ")"
	}
	return line
}

func formatTaskDetail(task *storage.Task) string {
	detail := formatTaskLine(task)
	if strings.TrimSpace(task.Body) != "" {
		detail += "\n\n" + task.Body
	}
	return detail + "\n"
}

func formatTaskCounts(tasks []*storage.Task) string {
	counts := map[string]int{}
	for _, task := range tasks {
		counts[task.Status]++
	}
	parts := []string{}
	for _, status := range []string{storage.TaskStatusIdea, storage.TaskStatusTodo, storage.TaskStatusInProgress,
		storage.TaskStatusInReview, storage.TaskStatusTesting, storage.TaskStatusDone, storage.TaskStatusCancelled} {
		if counts[status] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", status, counts[status]))
		}
	}
	parts = append(parts, fmt.Sprintf("total %d", len(tasks)))
	return "Counts: " + strings.Join(parts, ", ")
}

var _ tools.Tool = (*tasksTool)(nil)
