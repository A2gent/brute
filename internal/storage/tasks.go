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

type TaskImage struct {
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
}

// Task is always owned by exactly one project. Project-less tasks are intentionally unsupported.
type Task struct {
	ID            string     `json:"id"`
	ProjectID     string     `json:"project_id"`
	Ref           string     `json:"ref"`
	Seq           int        `json:"seq"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	Image         *TaskImage `json:"image,omitempty"`
	SessionID     string     `json:"session_id,omitempty"`
	Status        string     `json:"status"`
	Priority      int        `json:"priority"`
	Complexity    int        `json:"complexity"`
	DependencyIDs []string   `json:"dependency_ids"`
	Tags          []string   `json:"tags"`
	Price         string     `json:"price"`
	Position      float64    `json:"position"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	SourceKey     string     `json:"-"`
}

type TaskCreate struct {
	Title          string
	Body           string
	Image          *TaskImage
	Status         string
	Priority       int
	Complexity     int
	DependencyRefs []string
	Tags           []string
	Price          string
	Position       *float64
	CreatedBy      string
	SourceKey      string
}

type TaskUpdate struct {
	Title          *string
	Body           *string
	Image          **TaskImage
	SessionID      *string
	Status         *string
	Priority       *int
	Complexity     *int
	DependencyRefs *[]string
	Tags           *[]string
	Price          *string
	Position       *float64
}
