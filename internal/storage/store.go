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
	Summary      string // Concise one-sentence label for dense session lists.
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

// SessionTemplate stores reusable text for pre-filling new session prompts.
type SessionTemplate struct {
	ID        string
	Name      string
	Content   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RecurringJob represents a scheduled recurring job
type RecurringJob struct {
	ID               string
	ProjectID        *string // Optional project this job is tied to
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

// LeonardoGeneration tracks an async Leonardo image generation request
// initiated from a session tool call and later completed through webhook relay.
type LeonardoGeneration struct {
	ID            string
	SessionID     string
	ToolCallID    string
	IntegrationID string
	GenerationID  string
	Status        string // "pending" | "completed" | "failed"
	Prompt        string
	RequestJSON   string
	ResponseJSON  string
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
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

// AgentDefinitionRecord stores a unified agent definition (YAML) as a local
// installation. Saved sub_agents are also converted to Docker definitions at
// execution/export time; this table holds imported docker/remote definitions
// and their machine-specific bindings.
type AgentDefinitionRecord struct {
	ID             string
	Name           string
	Runtime        string // "docker" | "remote"
	DefinitionYAML string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SubAgent represents a reusable sub-agent configuration.
type SubAgent struct {
	ID                string
	Name              string
	ProjectID         *string  // Optional project this sub-agent is tied to
	Provider          string   // LLM provider type (e.g., "anthropic", "openai")
	Model             string   // Optional model override
	EnabledTools      []string // Tool names to enable (empty = all tools)
	InstructionBlocks string   // JSON-encoded instruction blocks array
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Project represents a session grouping container optionally tied to a folder.
type Project struct {
	ID          string
	Name        string
	Folder      *string           // Single optional folder path
	Settings    map[string]string // Project-scoped settings, such as project-only system prompt blocks.
	URLPatterns []string          // URLPattern-compatible absolute patterns used by browser extensions for project auto-detection.
	IsSystem    bool              // System projects (Knowledge Base, Agent) cannot be deleted
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ProjectPRDescription stores an editable pull request description for a project branch comparison.
type ProjectPRDescription struct {
	ProjectID  string
	RepoPath   string
	Branch     string
	BaseBranch string
	Content    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ProjectTestCache stores branch-scoped test and coverage API results.
type ProjectTestCache struct {
	ProjectID            string
	RepoPath             string
	Branch               string
	BaseBranch           string
	ScopeHash            string
	TestResponseJSON     string
	CoverageResponseJSON string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ProjectGitReviewOverlayCache stores generated line-level review explanations per changed file.
type ProjectGitReviewOverlayCache struct {
	ProjectID       string
	RepoPath        string
	Branch          string
	BaseBranch      string
	FilePath        string
	DiffHash        string
	AnnotationsJSON string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ProjectDatabase struct {
	ID          string
	ProjectID   string
	Name        string
	Engine      string // postgres, mysql, sqlite
	DSN         string
	Environment string
	IsReadOnly  bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store defines the interface for session storage
type Store interface {
	// Session operations
	SaveSession(sess *Session) error
	GetSession(id string) (*Session, error)
	GetSessionSummary(id string) (*Session, error)
	ListSessions() ([]*Session, error)                  // Returns only non-job sessions
	ListSessionsByJob(jobID string) ([]*Session, error) // Returns sessions for a specific job
	DeleteSession(id string) error

	// Session template operations
	SaveSessionTemplate(template *SessionTemplate) error
	GetSessionTemplate(id string) (*SessionTemplate, error)
	ListSessionTemplates() ([]*SessionTemplate, error)
	DeleteSessionTemplate(id string) error

	// Project operations
	SaveProject(project *Project) error
	GetProject(id string) (*Project, error)
	ListProjects() ([]*Project, error)
	DeleteProject(id string) error
	SaveProjectPRDescription(description *ProjectPRDescription) error
	GetProjectPRDescription(projectID string, repoPath string, branch string, baseBranch string) (*ProjectPRDescription, error)
	SaveProjectTestCache(cache *ProjectTestCache) error
	GetProjectTestCache(projectID string, repoPath string, branch string, baseBranch string, scopeHash string) (*ProjectTestCache, error)
	SaveProjectGitReviewOverlayCache(cache *ProjectGitReviewOverlayCache) error
	ListProjectGitReviewOverlayCache(projectID string, repoPath string, branch string, baseBranch string) ([]*ProjectGitReviewOverlayCache, error)

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

	// Leonardo async image generation operations
	SaveLeonardoGeneration(generation *LeonardoGeneration) error
	GetLeonardoGenerationByGenerationID(generationID string) (*LeonardoGeneration, error)
	ClaimLeonardoGenerationByGenerationID(generationID string, fromStatus string, toStatus string) (*LeonardoGeneration, bool, error)

	// MCP server operations
	SaveMCPServer(server *MCPServer) error
	GetMCPServer(id string) (*MCPServer, error)
	ListMCPServers() ([]*MCPServer, error)
	DeleteMCPServer(id string) error

	// Sub-agent operations
	SaveSubAgent(sa *SubAgent) error
	GetSubAgent(id string) (*SubAgent, error)
	ListSubAgents() ([]*SubAgent, error)
	DeleteSubAgent(id string) error

	// Stored unified agent definition operations
	SaveAgentDefinition(def *AgentDefinitionRecord) error
	GetAgentDefinition(id string) (*AgentDefinitionRecord, error)
	ListAgentDefinitions() ([]*AgentDefinitionRecord, error)
	DeleteAgentDefinition(id string) error

	// Project Database operations
	SaveProjectDatabase(db *ProjectDatabase) error
	GetProjectDatabase(id string) (*ProjectDatabase, error)
	ListProjectDatabases(projectID string) ([]*ProjectDatabase, error)
	DeleteProjectDatabase(id string) error

	// Close closes the store
	Close() error
}
