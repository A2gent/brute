package storage

import (
	"encoding/json"
	"time"
)

// Session represents a stored session (storage layer copy to avoid import cycle)
type Session struct {
	ID           string
	AgentID      string
	ParentID     *string
	JobID        *string // Associated recurring job (nil for regular sessions)
	ProjectID    *string // Associated project (nil for ungrouped sessions)
	Title        string
	Status       string
	Messages     []Message
	Metadata     map[string]interface{}
	TaskProgress string // Temporary task planning and progress tracking
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Message represents a stored message
type Message struct {
	ID          string
	Role        string
	Content     string
	ToolCalls   json.RawMessage
	ToolResults json.RawMessage
	Metadata    map[string]interface{}
	Timestamp   time.Time
}

// RecurringJob represents a scheduled recurring job
type RecurringJob struct {
	ID               string
	Name             string
	ScheduleHuman    string // Human-readable schedule (e.g., "every Monday at 9am")
	ScheduleCron     string // Parsed cron expression (e.g., "0 9 * * 1")
	TaskPrompt       string // The actual task instructions for the agent
	TaskPromptSource string // "text" | "file"
	TaskPromptFile   string // Absolute path when TaskPromptSource is "file"
	LLMProvider      string // Optional provider override for this job
	Enabled          bool
	LastRunAt        *time.Time
	NextRunAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// JobExecution represents a single execution of a recurring job
type JobExecution struct {
	ID         string
	JobID      string
	SessionID  string // Reference to the agent session created for this execution
	Status     string // "running", "success", "failed"
	Output     string // Summary of what the agent did
	Error      string // Error message if failed
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Integration represents an external channel integration configuration.
type Integration struct {
	ID        string
	Provider  string
	Name      string
	Mode      string // "notify_only" | "duplex"
	Enabled   bool
	Config    map[string]string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MCPServer represents a configured MCP server endpoint.
type MCPServer struct {
	ID                  string
	Name                string
	Transport           string // "stdio" | "http"
	Enabled             bool
	Config              map[string]string
	LastTestAt          *time.Time
	LastTestSuccess     *bool
	LastTestMessage     string
	LastEstimatedTokens *int
	LastToolCount       *int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Project represents a session grouping container optionally tied to a folder.
type Project struct {
	ID        string
	Name      string
	Folder    *string // Single optional folder path
	IsSystem  bool    // System projects (Knowledge Base, Agent) cannot be deleted
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Store defines the interface for session storage
type Store interface {
	// Session operations
	SaveSession(sess *Session) error
	GetSession(id string) (*Session, error)
	ListSessions() ([]*Session, error)                  // Returns only non-job sessions
	ListSessionsByJob(jobID string) ([]*Session, error) // Returns sessions for a specific job
	DeleteSession(id string) error

	// Project operations
	SaveProject(project *Project) error
	GetProject(id string) (*Project, error)
	ListProjects() ([]*Project, error)
	DeleteProject(id string) error

	// Recurring job operations
	SaveJob(job *RecurringJob) error
	GetJob(id string) (*RecurringJob, error)
	ListJobs() ([]*RecurringJob, error)
	DeleteJob(id string) error
	GetDueJobs(now time.Time) ([]*RecurringJob, error)

	// Job execution operations
	SaveJobExecution(exec *JobExecution) error
	GetJobExecution(id string) (*JobExecution, error)
	ListJobExecutions(jobID string, limit int) ([]*JobExecution, error)

	// Settings operations
	GetSettings() (map[string]string, error)
	SaveSettings(settings map[string]string) error

	// Integrations operations
	SaveIntegration(integration *Integration) error
	GetIntegration(id string) (*Integration, error)
	ListIntegrations() ([]*Integration, error)
	DeleteIntegration(id string) error

	// MCP server operations
	SaveMCPServer(server *MCPServer) error
	GetMCPServer(id string) (*MCPServer, error)
	ListMCPServers() ([]*MCPServer, error)
	DeleteMCPServer(id string) error

	// Close closes the store
	Close() error
}
