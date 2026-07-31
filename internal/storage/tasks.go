package storage

import "time"

const (
	TaskStatusIdea       = "idea"
	TaskStatusTodo       = "todo"
	TaskStatusInProgress = "in_progress"
	TaskStatusInReview   = "in_review"
	TaskStatusTesting    = "testing"
	TaskStatusDone       = "done"
	TaskStatusCancelled  = "cancelled"
)

var taskStatuses = map[string]struct{}{
	TaskStatusIdea: {}, TaskStatusTodo: {}, TaskStatusInProgress: {}, TaskStatusInReview: {},
	TaskStatusTesting: {}, TaskStatusDone: {}, TaskStatusCancelled: {},
}

// Task is always owned by exactly one project. Project-less tasks are intentionally unsupported.
type Task struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"project_id"`
	Ref         string     `json:"ref"`
	Seq         int        `json:"seq"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Status      string     `json:"status"`
	Priority    int        `json:"priority"`
	Complexity  int        `json:"complexity"`
	Tags        []string   `json:"tags"`
	Price       string     `json:"price"`
	Position    float64    `json:"position"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	SourceKey   string     `json:"-"`
}

type TaskCreate struct {
	Title      string
	Body       string
	Status     string
	Priority   int
	Complexity int
	Tags       []string
	Price      string
	Position   *float64
	CreatedBy  string
	SourceKey  string
}

type TaskUpdate struct {
	Title      *string
	Body       *string
	Status     *string
	Priority   *int
	Complexity *int
	Tags       *[]string
	Price      *string
	Position   *float64
}
