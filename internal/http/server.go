package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/llm/gemini"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/retry"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	skillsLoader "github.com/A2gent/brute/internal/skills"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
	"github.com/robfig/cron/v3"
)

// Server represents the HTTP API server
type Server struct {
	config         *config.Config
	llmClient      llm.Client
	toolManager    *tools.Manager
	sessionManager *session.Manager
	store          storage.Store
	router         chi.Router
	port           int
	speechClips    *speechcache.Store
	activeRunsMu   sync.Mutex
	activeRuns     map[string]map[string]context.CancelFunc
}

func (s *Server) resolveSessionWorkDir(sess *session.Session) string {
	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}

	if sess == nil || sess.ProjectID == nil {
		return defaultDir
	}

	projectID := strings.TrimSpace(*sess.ProjectID)
	if projectID == "" {
		return defaultDir
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		logging.Warn("Failed to load project for session workdir: session=%s project=%s error=%v", sess.ID, projectID, err)
		return defaultDir
	}

	if project.Folder != nil {
		candidate := strings.TrimSpace(*project.Folder)
		if candidate != "" {
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(defaultDir, candidate)
			}
			candidate = filepath.Clean(candidate)

			info, statErr := os.Stat(candidate)
			if statErr != nil || !info.IsDir() {
				logging.Warn("Skipping invalid project folder for session workdir: session=%s folder=%s", sess.ID, candidate)
				return defaultDir
			}
			return candidate
		}
	}

	return defaultDir
}

func (s *Server) toolManagerForSession(sess *session.Session) *tools.Manager {
	workDir := s.resolveSessionWorkDir(sess)
	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}
	if workDir == defaultDir {
		return s.toolManager
	}

	manager := tools.NewManager(workDir)
	integrationtools.Register(manager, s.store, s.speechClips)
	s.registerServerBackedTools(manager)
	return manager
}

func (s *Server) registerServerBackedTools(manager *tools.Manager) {
	if manager == nil {
		return
	}
	manager.Register(newRecurringJobsTool(s))
	manager.Register(newMCPManageTool(s))
	manager.RegisterQuestionTool(s.sessionManager)
	manager.RegisterSessionTaskProgressTool(s.sessionManager)
}

const thinkingJobIDSettingKey = "A2GENT_THINKING_JOB_ID"
const thinkingProjectID = "project-thinking"
const thinkingProjectName = "Thinking"
const thinkingSourceSettingKey = "A2GENT_THINKING_SOURCE"
const thinkingTextSettingKey = "A2GENT_THINKING_TEXT"
const thinkingFilePathSettingKey = "A2GENT_THINKING_FILE_PATH"
const thinkingInstructionBlocksSettingKey = "A2GENT_THINKING_INSTRUCTION_BLOCKS"
const agentInstructionBlocksSettingKey = "A2GENT_AGENT_INSTRUCTION_BLOCKS"
const builtInToolsInstructionBlockType = "builtin_tools"
const integrationSkillsInstructionBlockType = "integration_skills"
const externalMarkdownSkillsInstructionBlockType = "external_markdown_skills"
const mcpServersInstructionBlockType = "mcp_servers"
const skillsFolderSettingKey = "AAGENT_SKILLS_FOLDER"
const defaultDynamicInstructionFile = "AGENTS.md"
const maxDynamicInstructionBytes = 32 * 1024
const sessionSystemPromptSnapshotMetadataKey = "system_prompt_snapshot"
const thinkingRunTaskPrompt = "Run the Thinking routine.\n\nReview the current project state, execute the most valuable next step, and summarize outcomes."

// NewServer creates a new HTTP server instance
func NewServer(
	cfg *config.Config,
	llmClient llm.Client,
	toolManager *tools.Manager,
	sessionManager *session.Manager,
	store storage.Store,
	speechClips *speechcache.Store,
	port int,
) *Server {
	if speechClips == nil {
		speechClips = speechcache.New(0)
	}
	s := &Server{
		config:         cfg,
		llmClient:      llmClient,
		toolManager:    toolManager,
		sessionManager: sessionManager,
		store:          store,
		port:           port,
		speechClips:    speechClips,
		activeRuns:     make(map[string]map[string]context.CancelFunc),
	}

	s.registerServerBackedTools(s.toolManager)
	s.setupRoutes()
	return s
}

// setupRoutes configures all API routes
func (s *Server) setupRoutes() {
	r := chi.NewRouter()

	// Middleware (no logger to avoid polluting TUI output)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(5 * time.Minute))

	// CORS configuration - allow all origins for flexibility
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false, // Must be false when AllowedOrigins is "*"
		MaxAge:           300,
	}))

	// Health check
	r.Get("/health", s.handleHealth)

	// App settings (tokens/secrets/runtime options)
	r.Get("/settings", s.handleGetSettings)
	r.Put("/settings", s.handleUpdateSettings)
	r.Post("/settings/instruction-estimate", s.handleEstimateInstructionPrompt)

	// LLM provider configuration
	r.Route("/providers", func(r chi.Router) {
		r.Get("/", s.handleListProviders)
		r.Put("/active", s.handleSetActiveProvider)
		r.Post("/fallback-aggregates", s.handleCreateFallbackAggregate)
		r.Get("/lmstudio/models", s.handleListLMStudioModels)
		r.Get("/kimi/models", s.handleListKimiModels)
		r.Get("/google/models", s.handleListGoogleModels)
		r.Get("/openai/models", s.handleListOpenAIModels)
		r.Get("/openrouter/models", s.handleListOpenRouterModels)
		r.Get("/anthropic/models", s.handleListAnthropicModels)
		r.Put("/{providerType}", s.handleUpdateProvider)
		r.Delete("/{providerType}", s.handleDeleteProvider)

		// Anthropic OAuth
		r.Post("/anthropic/oauth/start", s.handleAnthropicOAuthStart)
		r.Post("/anthropic/oauth/callback", s.handleAnthropicOAuthCallback)
		r.Get("/anthropic/oauth/status", s.handleAnthropicOAuthStatus)
		r.Delete("/anthropic/oauth", s.handleAnthropicOAuthDisconnect)
	})

	// External channel integrations
	r.Route("/integrations", func(r chi.Router) {
		r.Get("/", s.handleListIntegrations)
		r.Post("/", s.handleCreateIntegration)
		r.Post("/telegram/chat-ids", s.handleDiscoverTelegramChats)
		r.Get("/{integrationID}", s.handleGetIntegration)
		r.Put("/{integrationID}", s.handleUpdateIntegration)
		r.Delete("/{integrationID}", s.handleDeleteIntegration)
		r.Post("/{integrationID}/test", s.handleTestIntegration)
	})

	// MCP server registry and diagnostics
	r.Route("/mcp/servers", func(r chi.Router) {
		r.Get("/", s.handleListMCPServers)
		r.Post("/", s.handleCreateMCPServer)
		r.Get("/{serverID}", s.handleGetMCPServer)
		r.Put("/{serverID}", s.handleUpdateMCPServer)
		r.Delete("/{serverID}", s.handleDeleteMCPServer)
		r.Post("/{serverID}/test", s.handleTestMCPServer)
	})

	// Speech/TTS helpers (proxied through backend)
	r.Route("/speech", func(r chi.Router) {
		r.Get("/voices", s.handleListSpeechVoices)
		r.Get("/piper/voices", s.handleListPiperVoices)
		r.Post("/completion", s.handleCompletionSpeech)
		r.Post("/transcribe", s.handleTranscribeSpeech)
		r.Get("/clips/{clipID}", s.handleGetSpeechClip)
	})

	// Local assets exposed for session UI rendering.
	r.Route("/assets", func(r chi.Router) {
		r.Get("/images", s.handleGetImageAsset)
	})

	// Local device helpers.
	r.Route("/devices", func(r chi.Router) {
		r.Get("/cameras", s.handleListCameraDevices)
	})

	// Browser Chrome tool management
	r.Route("/browser-chrome", func(r chi.Router) {
		r.Get("/profile-status", s.handleBrowserChromeProfileStatus)
		r.Post("/create-profile", s.handleBrowserChromeCreateProfile)
		r.Post("/launch", s.handleBrowserChromeLaunch)
	})

	// Session endpoints
	r.Route("/sessions", func(r chi.Router) {
		r.Get("/", s.handleListSessions)
		r.Post("/", s.handleCreateSession)
		r.Get("/{sessionID}", s.handleGetSession)
		r.Delete("/{sessionID}", s.handleDeleteSession)
		r.Post("/{sessionID}/cancel", s.handleCancelSession)
		r.Put("/{sessionID}/project", s.handleUpdateSessionProject)
		r.Post("/{sessionID}/chat", s.handleChat)
		r.Post("/{sessionID}/chat/stream", s.handleChatStream)
		r.Get("/{sessionID}/question", s.handleGetPendingQuestion)
		r.Post("/{sessionID}/answer", s.handleAnswerQuestion)
		r.Post("/{sessionID}/start", s.handleStartSession)
		r.Get("/{sessionID}/task-progress", s.handleGetTaskProgress)
	})

	// Projects endpoints (optional grouping for sessions)
	r.Route("/projects", func(r chi.Router) {
		r.Get("/", s.handleListProjects)
		r.Post("/", s.handleCreateProject)
		// Static routes must come before dynamic {projectID} route
		r.Get("/tree", s.handleListProjectTree)
		r.Get("/file", s.handleGetProjectFile)
		r.Post("/file", s.handleUpsertProjectFile)
		r.Put("/file", s.handleUpsertProjectFile)
		r.Delete("/file", s.handleDeleteProjectFile)
		r.Post("/file/move", s.handleMoveProjectFile)
		r.Post("/folder", s.handleCreateProjectFolder)
		r.Post("/rename", s.handleRenameProjectEntry)
		// Dynamic route must come last
		r.Get("/{projectID}", s.handleGetProject)
		r.Put("/{projectID}", s.handleUpdateProject)
		r.Delete("/{projectID}", s.handleDeleteProject)
	})

	// Recurring jobs endpoints
	r.Route("/jobs", func(r chi.Router) {
		r.Get("/", s.handleListJobs)
		r.Post("/", s.handleCreateJob)
		r.Get("/{jobID}", s.handleGetJob)
		r.Put("/{jobID}", s.handleUpdateJob)
		r.Delete("/{jobID}", s.handleDeleteJob)
		r.Post("/{jobID}/run", s.handleRunJobNow)
		r.Get("/{jobID}/executions", s.handleListJobExecutions)
		r.Get("/{jobID}/sessions", s.handleListJobSessions)
	})

	// My Mind filesystem endpoints
	r.Route("/mind", func(r chi.Router) {
		r.Get("/config", s.handleGetMindConfig)
		r.Put("/config", s.handleUpdateMindConfig)
		r.Get("/browse", s.handleBrowseMindDirectories)
		r.Get("/tree", s.handleListMindTree)
		r.Get("/file", s.handleGetMindFile)
		r.Post("/file", s.handleUpsertMindFile)
		r.Put("/file", s.handleUpsertMindFile)
		r.Delete("/file", s.handleDeleteMindFile)
		r.Post("/file/move", s.handleMoveMindFile)
		r.Post("/folder", s.handleCreateMindFolder)
		r.Post("/rename", s.handleRenameMindEntry)
	})

	// Skills helpers (folder selection and markdown discovery)
	r.Route("/skills", func(r chi.Router) {
		r.Get("/builtin", s.handleListBuiltInSkills)
		r.Get("/integration-backed", s.handleListIntegrationBackedSkills)
		r.Get("/browse", s.handleBrowseSkillDirectories)
		r.Get("/discover", s.handleDiscoverSkills)
		r.Get("/registry/search", s.handleSearchRegistry)
		r.Post("/registry/install", s.handleInstallSkill)
		r.Delete("/delete", s.handleDeleteSkill)
	})

	s.router = r
}

// Run starts the HTTP server
func (s *Server) Run(ctx context.Context) error {
	addr := fmt.Sprintf("0.0.0.0:%d", s.port)
	logging.Info("Starting HTTP server on %s", addr)
	fmt.Printf("HTTP API server running on http://0.0.0.0:%d (accessible from any host)\n", s.port)

	go s.runTelegramDuplexLoop(ctx)

	server := &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	// Handle graceful shutdown
	go func() {
		<-ctx.Done()
		logging.Info("Shutting down HTTP server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	return server.ListenAndServe()
}

// --- Request/Response types ---

// CreateSessionRequest represents a request to create a new session
type CreateSessionRequest struct {
	AgentID   string `json:"agent_id"`
	Task      string `json:"task,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Queued    bool   `json:"queued,omitempty"` // If true, create session without starting it
}

// CreateSessionResponse represents a response after creating a session
type CreateSessionResponse struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	ProjectID string    `json:"project_id,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// SessionResponse represents a session with its messages
type SessionResponse struct {
	ID                   string                       `json:"id"`
	AgentID              string                       `json:"agent_id"`
	ParentID             string                       `json:"parent_id,omitempty"`
	ProjectID            string                       `json:"project_id,omitempty"`
	Provider             string                       `json:"provider,omitempty"`
	Model                string                       `json:"model,omitempty"`
	RoutedProvider       string                       `json:"routed_provider,omitempty"`
	RoutedModel          string                       `json:"routed_model,omitempty"`
	Title                string                       `json:"title"`
	Status               string                       `json:"status"`
	TotalTokens          int                          `json:"total_tokens"`
	InputTokens          int                          `json:"input_tokens"`
	OutputTokens         int                          `json:"output_tokens"`
	CurrentContextTokens int                          `json:"current_context_tokens"`
	ModelContextWindow   int                          `json:"model_context_window"`
	TaskProgress         string                       `json:"task_progress,omitempty"`
	CreatedAt            time.Time                    `json:"created_at"`
	UpdatedAt            time.Time                    `json:"updated_at"`
	Messages             []MessageResponse            `json:"messages"`
	SystemPromptSnapshot *SystemPromptSnapshotPayload `json:"system_prompt_snapshot,omitempty"`
}

type SystemPromptSnapshotPayload struct {
	BasePrompt        string                             `json:"base_prompt"`
	CombinedPrompt    string                             `json:"combined_prompt"`
	BaseEstimated     int                                `json:"base_estimated_tokens"`
	CombinedEstimated int                                `json:"combined_estimated_tokens"`
	Blocks            []SystemPromptBlockSnapshotPayload `json:"blocks"`
}

type SystemPromptBlockSnapshotPayload struct {
	Type            string `json:"type"`
	Value           string `json:"value"`
	Enabled         bool   `json:"enabled"`
	ResolvedContent string `json:"resolved_content,omitempty"`
	SourcePath      string `json:"source_path,omitempty"`
	Error           string `json:"error,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

// MessageResponse represents a message in a session
type MessageResponse struct {
	Role         string                 `json:"role"`
	Content      string                 `json:"content"`
	ToolCalls    []ToolCallResponse     `json:"tool_calls,omitempty"`
	ToolResults  []ToolResultResponse   `json:"tool_results,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Timestamp    time.Time              `json:"timestamp"`
	InputTokens  int                    `json:"input_tokens,omitempty"`
	OutputTokens int                    `json:"output_tokens,omitempty"`
}

// ToolCallResponse represents a tool call
type ToolCallResponse struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	InputTokens  int             `json:"input_tokens,omitempty"`
	OutputTokens int             `json:"output_tokens,omitempty"`
}

// ToolResultResponse represents a tool result
type ToolResultResponse struct {
	ToolCallID string                 `json:"tool_call_id"`
	Content    string                 `json:"content"`
	IsError    bool                   `json:"is_error"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Name       string                 `json:"name,omitempty"` // Tool name (required by Gemini)
}

// ChatRequest represents a chat message request
type ChatRequest struct {
	Message string `json:"message"`
}

// ChatResponse represents a chat response
type ChatResponse struct {
	Content  string            `json:"content"`
	Messages []MessageResponse `json:"messages"`
	Status   string            `json:"status"`
	Usage    UsageResponse     `json:"usage"`
}

type ChatStreamEvent struct {
	Type       string                 `json:"type"`
	Delta      string                 `json:"delta,omitempty"`
	Content    string                 `json:"content,omitempty"`
	Messages   []MessageResponse      `json:"messages,omitempty"`
	Status     string                 `json:"status,omitempty"`
	Usage      *UsageResponse         `json:"usage,omitempty"`
	Error      string                 `json:"error,omitempty"`
	ToolCalls  []StreamToolCallEvent  `json:"tool_calls,omitempty"`
	ToolResult *StreamToolResultEvent `json:"tool_result,omitempty"`
	Step       int                    `json:"step,omitempty"`
}

// StreamToolCallEvent represents a tool call in a stream event.
type StreamToolCallEvent struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// StreamToolResultEvent represents a tool result in a stream event.
type StreamToolResultEvent struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

// UsageResponse represents token usage
type UsageResponse struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SessionListItem represents a session in the list
type SessionListItem struct {
	ID                 string    `json:"id"`
	AgentID            string    `json:"agent_id"`
	ProjectID          string    `json:"project_id,omitempty"`
	Provider           string    `json:"provider,omitempty"`
	Model              string    `json:"model,omitempty"`
	RoutedProvider     string    `json:"routed_provider,omitempty"`
	RoutedModel        string    `json:"routed_model,omitempty"`
	Title              string    `json:"title"`
	Status             string    `json:"status"`
	TotalTokens        int       `json:"total_tokens"`
	InputTokens        int       `json:"input_tokens"`
	OutputTokens       int       `json:"output_tokens"`
	RunDurationSeconds int64     `json:"run_duration_seconds"`
	TaskProgress       string    `json:"task_progress,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// --- Recurring Jobs Request/Response types ---

// CreateJobRequest represents a request to create a recurring job
type CreateJobRequest struct {
	Name             string `json:"name"`
	ScheduleText     string `json:"schedule_text"` // Natural language schedule
	TaskPrompt       string `json:"task_prompt"`
	TaskPromptSource string `json:"task_prompt_source,omitempty"` // "text" | "file"
	TaskPromptFile   string `json:"task_prompt_file,omitempty"`
	LLMProvider      string `json:"llm_provider,omitempty"`
	Enabled          bool   `json:"enabled"`
}

// UpdateJobRequest represents a request to update a recurring job
type UpdateJobRequest struct {
	Name             string  `json:"name"`
	ScheduleText     string  `json:"schedule_text"`
	TaskPrompt       string  `json:"task_prompt"`
	TaskPromptSource string  `json:"task_prompt_source,omitempty"` // "text" | "file"
	TaskPromptFile   string  `json:"task_prompt_file,omitempty"`
	LLMProvider      *string `json:"llm_provider,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
}

// JobResponse represents a recurring job response
type JobResponse struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	ScheduleHuman    string     `json:"schedule_human"`
	ScheduleCron     string     `json:"schedule_cron"`
	TaskPrompt       string     `json:"task_prompt"`
	TaskPromptSource string     `json:"task_prompt_source"`
	TaskPromptFile   string     `json:"task_prompt_file,omitempty"`
	LLMProvider      string     `json:"llm_provider,omitempty"`
	Enabled          bool       `json:"enabled"`
	LastRunAt        *time.Time `json:"last_run_at,omitempty"`
	NextRunAt        *time.Time `json:"next_run_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// JobExecutionResponse represents a job execution response
type JobExecutionResponse struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	SessionID  string     `json:"session_id,omitempty"`
	Status     string     `json:"status"`
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type SettingsResponse struct {
	Settings                               map[string]string `json:"settings"`
	DefaultSystemPrompt                    string            `json:"defaultSystemPrompt"`
	DefaultSystemPromptWithoutBuiltInTools string            `json:"defaultSystemPromptWithoutBuiltInTools"`
}

type UpdateSettingsRequest struct {
	Settings map[string]string `json:"settings"`
}

type ProviderConfigResponse struct {
	Type           string                     `json:"type"`
	DisplayName    string                     `json:"display_name"`
	DefaultURL     string                     `json:"default_url"`
	RequiresKey    bool                       `json:"requires_key"`
	DefaultModel   string                     `json:"default_model"`
	ContextWindow  int                        `json:"context_window"`
	IsActive       bool                       `json:"is_active"`
	Configured     bool                       `json:"configured"`
	HasAPIKey      bool                       `json:"has_api_key"`
	BaseURL        string                     `json:"base_url"`
	Model          string                     `json:"model"`
	FallbackChain  []config.FallbackChainNode `json:"fallback_chain,omitempty"`
	RouterProvider string                     `json:"router_provider,omitempty"`
	RouterModel    string                     `json:"router_model,omitempty"`
	RouterRules    []config.RouterRule        `json:"router_rules,omitempty"`
}

type UpdateProviderRequest struct {
	Name           *string                     `json:"name,omitempty"`
	APIKey         *string                     `json:"api_key,omitempty"`
	BaseURL        *string                     `json:"base_url,omitempty"`
	Model          *string                     `json:"model,omitempty"`
	FallbackChain  *[]config.FallbackChainNode `json:"fallback_chain,omitempty"`
	RouterProvider *string                     `json:"router_provider,omitempty"`
	RouterModel    *string                     `json:"router_model,omitempty"`
	RouterRules    *[]config.RouterRule        `json:"router_rules,omitempty"`
	Active         *bool                       `json:"active,omitempty"`
}

type SetActiveProviderRequest struct {
	Provider string `json:"provider"`
}

type CreateFallbackAggregateRequest struct {
	Name          string                     `json:"name"`
	FallbackChain []config.FallbackChainNode `json:"fallback_chain"`
	Active        bool                       `json:"active,omitempty"`
}

type ListProviderModelsResponse struct {
	Models []string `json:"models"`
}

type UpdateSessionProjectRequest struct {
	ProjectID *string `json:"project_id"`
}

type ProjectResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Folder    *string   `json:"folder,omitempty"`
	IsSystem  bool      `json:"is_system"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateProjectRequest struct {
	Name   string  `json:"name"`
	Folder *string `json:"folder,omitempty"`
}

type UpdateProjectRequest struct {
	Name   *string `json:"name,omitempty"`
	Folder *string `json:"folder,omitempty"`
}

// --- Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load settings: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, settingsResponse(settings))
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Settings == nil {
		req.Settings = map[string]string{}
	}

	oldSettings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load existing settings: "+err.Error())
		return
	}

	if err := s.store.SaveSettings(req.Settings); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save settings: "+err.Error())
		return
	}

	syncSettingsToEnv(oldSettings, req.Settings)
	s.jsonResponse(w, http.StatusOK, settingsResponse(req.Settings))
}

func settingsResponse(settings map[string]string) SettingsResponse {
	return SettingsResponse{
		Settings:                               settings,
		DefaultSystemPrompt:                    agent.DefaultSystemPrompt(),
		DefaultSystemPromptWithoutBuiltInTools: agent.DefaultSystemPromptWithoutBuiltInTools(),
	}
}

func (s *Server) handleEstimateInstructionPrompt(w http.ResponseWriter, r *http.Request) {
	var req UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	settings := req.Settings
	if settings == nil {
		loaded, err := s.store.GetSettings()
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to load settings: "+err.Error())
			return
		}
		settings = loaded
	}

	snapshot := s.composeSystemPromptSnapshotWithSettings(nil, settings)
	if snapshot == nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to compose instruction snapshot")
		return
	}

	blocks := make([]SystemPromptBlockSnapshotPayload, len(snapshot.Blocks))
	for i, block := range snapshot.Blocks {
		blocks[i] = SystemPromptBlockSnapshotPayload{
			Type:            block.Type,
			Value:           block.Value,
			Enabled:         block.Enabled,
			ResolvedContent: block.ResolvedContent,
			SourcePath:      block.SourcePath,
			Error:           block.Error,
			EstimatedTokens: block.EstimatedTokens,
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"snapshot": SystemPromptSnapshotPayload{
			BasePrompt:        snapshot.BasePrompt,
			CombinedPrompt:    snapshot.CombinedPrompt,
			BaseEstimated:     snapshot.BaseEstimated,
			CombinedEstimated: snapshot.CombinedEstimated,
			Blocks:            blocks,
		},
	})
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	definitions := config.SupportedProviders()
	resp := make([]ProviderConfigResponse, 0, len(definitions)+len(s.config.FallbackAggregates))

	for _, def := range definitions {
		existing := s.config.Providers[string(def.Type)]
		if def.Type == config.ProviderFallback {
			chain := normalizeFallbackChainNodes(existing.FallbackChainNodes)
			if len(chain) == 0 && len(existing.FallbackChain) > 0 {
				chain = legacyProvidersToFallbackNodes(existing.FallbackChain, s.resolveModelForProvider)
			}
			isActive := config.NormalizeProviderRef(s.config.ActiveProvider) == string(def.Type)
			if !isActive && len(chain) == 0 {
				// Legacy built-in fallback aggregate is hidden unless it is actively used.
				continue
			}
			resp = append(resp, ProviderConfigResponse{
				Type:          string(def.Type),
				DisplayName:   def.DisplayName,
				DefaultURL:    def.DefaultURL,
				RequiresKey:   def.RequiresKey,
				DefaultModel:  def.DefaultModel,
				ContextWindow: s.resolveContextWindowForProvider(def.Type),
				IsActive:      isActive,
				Configured:    s.fallbackChainIsConfigured(chain),
				HasAPIKey:     false,
				BaseURL:       "",
				Model:         "",
				FallbackChain: chain,
			})
			continue
		}
		if def.Type == config.ProviderAutoRouter {
			rules := normalizeRouterRules(existing.RouterRules)
			resp = append(resp, ProviderConfigResponse{
				Type:           string(def.Type),
				DisplayName:    def.DisplayName,
				DefaultURL:     def.DefaultURL,
				RequiresKey:    def.RequiresKey,
				DefaultModel:   def.DefaultModel,
				ContextWindow:  s.resolveContextWindowForProvider(def.Type),
				IsActive:       config.NormalizeProviderRef(s.config.ActiveProvider) == string(def.Type),
				Configured:     s.autoRouterConfigured(existing),
				HasAPIKey:      false,
				BaseURL:        "",
				Model:          "",
				FallbackChain:  nil,
				RouterProvider: config.NormalizeProviderRef(existing.RouterProvider),
				RouterModel:    strings.TrimSpace(existing.RouterModel),
				RouterRules:    rules,
			})
			continue
		}

		baseURL := strings.TrimSpace(existing.BaseURL)
		if baseURL == "" {
			baseURL = def.DefaultURL
		}
		model := strings.TrimSpace(existing.Model)
		if model == "" {
			model = def.DefaultModel
		}

		configured := baseURL != ""
		hasAPIKey := strings.TrimSpace(existing.APIKey) != ""
		hasOAuth := existing.OAuth != nil && existing.OAuth.AccessToken != ""

		if def.RequiresKey {
			// Provider is configured if it has API key OR OAuth tokens
			configured = configured && (hasAPIKey || hasOAuth)
		}

		resp = append(resp, ProviderConfigResponse{
			Type:          string(def.Type),
			DisplayName:   def.DisplayName,
			DefaultURL:    def.DefaultURL,
			RequiresKey:   def.RequiresKey,
			DefaultModel:  def.DefaultModel,
			ContextWindow: def.ContextWindow,
			IsActive:      s.config.ActiveProvider == string(def.Type),
			Configured:    configured,
			HasAPIKey:     hasAPIKey,
			BaseURL:       baseURL,
			Model:         model,
			FallbackChain: nil,
		})
	}

	for _, aggregate := range s.config.FallbackAggregates {
		providerRef := config.FallbackAggregateRefFromID(aggregate.ID)
		chain := normalizeFallbackChainNodes(aggregate.Chain)
		resp = append(resp, ProviderConfigResponse{
			Type:          providerRef,
			DisplayName:   strings.TrimSpace(aggregate.Name),
			DefaultURL:    "",
			RequiresKey:   false,
			DefaultModel:  "",
			ContextWindow: s.resolveContextWindowForProvider(config.ProviderType(providerRef)),
			IsActive:      config.NormalizeProviderRef(s.config.ActiveProvider) == providerRef,
			Configured:    s.fallbackChainIsConfigured(chain),
			HasAPIKey:     false,
			BaseURL:       "",
			Model:         "",
			FallbackChain: chain,
		})
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	providerType := config.ProviderType(config.NormalizeProviderRef(chi.URLParam(r, "providerType")))

	var req UpdateProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if config.IsFallbackAggregateRef(string(providerType)) {
		aggregate, _ := s.findFallbackAggregateByRef(string(providerType))
		if aggregate == nil {
			s.errorResponse(w, http.StatusNotFound, "Fallback aggregate not found: "+string(providerType))
			return
		}

		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				s.errorResponse(w, http.StatusBadRequest, "Name cannot be empty")
				return
			}
			aggregate.Name = name
		}
		if req.FallbackChain != nil {
			chain, err := s.normalizeAndValidateFallbackChain(*req.FallbackChain)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			aggregate.Chain = chain
		}
		if req.Active != nil && *req.Active {
			s.config.ActiveProvider = string(providerType)
		}
		if err := s.config.Save(config.GetConfigPath()); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
			return
		}
		s.handleListProviders(w, r)
		return
	}

	def := config.GetProviderDefinition(providerType)
	if def == nil {
		s.errorResponse(w, http.StatusBadRequest, "Unsupported provider: "+string(providerType))
		return
	}

	provider := s.config.Providers[string(providerType)]
	provider.Name = string(providerType)
	if providerType == config.ProviderFallback {
		if req.FallbackChain != nil {
			chain, err := s.normalizeAndValidateFallbackChain(*req.FallbackChain)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			provider.FallbackChainNodes = chain
			provider.FallbackChain = nil
		}
		provider.APIKey = ""
		provider.BaseURL = ""
		provider.Model = ""
		provider.RouterProvider = ""
		provider.RouterModel = ""
		provider.RouterRules = nil
	} else if providerType == config.ProviderAutoRouter {
		if req.RouterProvider != nil {
			provider.RouterProvider = config.NormalizeProviderRef(*req.RouterProvider)
		}
		if req.RouterModel != nil {
			provider.RouterModel = strings.TrimSpace(*req.RouterModel)
		}
		if req.RouterRules != nil {
			rules, err := s.normalizeAndValidateRouterRules(*req.RouterRules)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			provider.RouterRules = rules
		}
		if err := s.validateAutoRouterProvider(provider); err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		provider.APIKey = ""
		provider.BaseURL = ""
		provider.Model = ""
		provider.FallbackChain = nil
		provider.FallbackChainNodes = nil
	} else {
		if req.APIKey != nil {
			provider.APIKey = strings.TrimSpace(*req.APIKey)
		}
		if req.BaseURL != nil {
			baseURL := strings.TrimSpace(*req.BaseURL)
			if providerType == config.ProviderLMStudio || providerType == config.ProviderOpenRouter || providerType == config.ProviderGoogle || providerType == config.ProviderOpenAI {
				baseURL = normalizeOpenAIBaseURL(baseURL)
			}
			provider.BaseURL = baseURL
		}
		if req.Model != nil {
			provider.Model = strings.TrimSpace(*req.Model)
		}

		if provider.BaseURL == "" {
			provider.BaseURL = def.DefaultURL
		}
		if provider.Model == "" {
			provider.Model = def.DefaultModel
		}
		provider.RouterProvider = ""
		provider.RouterModel = ""
		provider.RouterRules = nil
	}

	s.config.SetProvider(providerType, provider)

	if req.Active != nil && *req.Active {
		s.config.ActiveProvider = string(providerType)
		if providerType != config.ProviderAutoRouter && provider.Model != "" {
			s.config.DefaultModel = provider.Model
		}
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}

	s.handleListProviders(w, r)
}

func (s *Server) handleSetActiveProvider(w http.ResponseWriter, r *http.Request) {
	var req SetActiveProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	providerType := config.ProviderType(config.NormalizeProviderRef(req.Provider))
	def := config.GetProviderDefinition(providerType)
	if def == nil && !s.providerRefExists(string(providerType)) {
		s.errorResponse(w, http.StatusBadRequest, "Unsupported provider: "+req.Provider)
		return
	}

	s.config.ActiveProvider = string(providerType)
	provider := s.config.Providers[string(providerType)]
	if def != nil && providerType != config.ProviderAutoRouter && provider.Model != "" {
		s.config.DefaultModel = provider.Model
	} else if def != nil && providerType != config.ProviderAutoRouter && def.DefaultModel != "" {
		s.config.DefaultModel = def.DefaultModel
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}

	s.handleListProviders(w, r)
}

func (s *Server) handleCreateFallbackAggregate(w http.ResponseWriter, r *http.Request) {
	var req CreateFallbackAggregateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return
	}

	chain, err := s.normalizeAndValidateFallbackChain(req.FallbackChain)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	id := config.NormalizeToken(name)
	if id == "" {
		id = "aggregate"
	}
	baseID := id
	suffix := 2
	for s.findFallbackAggregateByID(id) != nil {
		id = fmt.Sprintf("%s-%d", baseID, suffix)
		suffix++
	}

	aggregate := config.FallbackAggregate{
		ID:    id,
		Name:  name,
		Chain: chain,
	}
	s.config.FallbackAggregates = append(s.config.FallbackAggregates, aggregate)
	if req.Active {
		s.config.ActiveProvider = config.FallbackAggregateRefFromID(id)
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}

	s.handleListProviders(w, r)
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if providerRef != string(config.ProviderFallback) && !config.IsFallbackAggregateRef(providerRef) {
		s.errorResponse(w, http.StatusBadRequest, "Only fallback aggregates can be deleted")
		return
	}

	if config.NormalizeProviderRef(s.config.ActiveProvider) == providerRef {
		s.errorResponse(w, http.StatusBadRequest, "Cannot delete active provider. Set another provider active first.")
		return
	}

	jobs, err := s.store.ListJobs()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to check jobs: "+err.Error())
		return
	}
	for _, job := range jobs {
		if config.NormalizeProviderRef(job.LLMProvider) == providerRef {
			s.errorResponse(w, http.StatusConflict, fmt.Sprintf("Cannot delete provider: recurring job %q (%s) uses it", job.Name, job.ID))
			return
		}
	}

	if providerRef == string(config.ProviderFallback) {
		provider := s.config.Providers[string(config.ProviderFallback)]
		provider.FallbackChain = nil
		provider.FallbackChainNodes = nil
		s.config.SetProvider(config.ProviderFallback, provider)
	} else {
		aggregate, index := s.findFallbackAggregateByRef(providerRef)
		if aggregate == nil || index < 0 {
			s.errorResponse(w, http.StatusNotFound, "Fallback aggregate not found: "+providerRef)
			return
		}
		s.config.FallbackAggregates = append(s.config.FallbackAggregates[:index], s.config.FallbackAggregates[index+1:]...)
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListLMStudioModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderLMStudio, "LM Studio")
}

func (s *Server) handleListKimiModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderKimi, "Kimi")
}

func (s *Server) handleListGoogleModels(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderGoogle)]
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(config.ProviderGoogle)
	}

	baseURL := normalizeOpenAIBaseURL(provider.BaseURL)
	if baseURL == "" {
		baseURL = normalizeOpenAIBaseURL(config.GetProviderDefinition(config.ProviderGoogle).DefaultURL)
	}

	models, err := gemini.ListModels(apiKey, baseURL)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list Google models: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{
		Models: models,
	})
}

func (s *Server) handleListOpenAIModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderOpenAI, "OpenAI")
}

func (s *Server) handleListOpenRouterModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderOpenRouter, "OpenRouter")
}

func (s *Server) handleListAnthropicModels(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderAnthropic)]
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(config.ProviderAnthropic)
	}

	models, err := anthropic.ListModels(apiKey)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list Anthropic models: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{
		Models: models,
	})
}

func (s *Server) handleListOpenAICompatibleModels(w http.ResponseWriter, r *http.Request, providerType config.ProviderType, providerName string) {
	def := config.GetProviderDefinition(providerType)
	baseURL := normalizeOpenAIBaseURL(r.URL.Query().Get("base_url"))
	if baseURL == "" {
		provider := s.config.Providers[string(providerType)]
		baseURL = normalizeOpenAIBaseURL(provider.BaseURL)
	}
	if baseURL == "" && def != nil {
		baseURL = normalizeOpenAIBaseURL(def.DefaultURL)
	}
	if baseURL == "" {
		s.errorResponse(w, http.StatusBadRequest, providerName+" base URL is not configured")
		return
	}

	provider := s.config.Providers[string(providerType)]
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(providerType)
	}
	if def != nil && def.RequiresKey && apiKey == "" {
		s.errorResponse(w, http.StatusBadRequest, providerName+" API key is not configured")
		return
	}

	client := lmstudio.NewClient(apiKey, "", baseURL)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	models, err := client.ListModels(ctx)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to fetch models from "+providerName+": "+err.Error())
		return
	}

	modelIDs := make([]string, 0, len(models))
	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID != "" {
			modelIDs = append(modelIDs, modelID)
		}
	}

	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{Models: modelIDs})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.sessionManager.List()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sessions: "+err.Error())
		return
	}

	items := make([]SessionListItem, len(sessions))
	for i, sess := range sessions {
		provider, model := sessionProviderAndModel(sess)
		routedProvider, routedModel := sessionRoutedProviderAndModel(sess)
		projectID := ""
		if sess.ProjectID != nil {
			projectID = *sess.ProjectID
		}
		inputTokens, outputTokens := sessionInputOutputTokens(sess)
		items[i] = SessionListItem{
			ID:                 sess.ID,
			AgentID:            sess.AgentID,
			ProjectID:          projectID,
			Provider:           provider,
			Model:              model,
			RoutedProvider:     routedProvider,
			RoutedModel:        routedModel,
			Title:              sess.Title,
			Status:             string(sess.Status),
			TotalTokens:        inputTokens + outputTokens,
			InputTokens:        inputTokens,
			OutputTokens:       outputTokens,
			RunDurationSeconds: sessionRunDurationSeconds(sess.CreatedAt, sess.UpdatedAt, string(sess.Status)),
			TaskProgress:       sess.TaskProgress,
			CreatedAt:          sess.CreatedAt,
			UpdatedAt:          sess.UpdatedAt,
		}
	}

	s.jsonResponse(w, http.StatusOK, items)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.AgentID == "" {
		req.AgentID = "build" // Default agent
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID != "" {
		if _, err := s.store.GetProject(req.ProjectID); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Project not found: "+err.Error())
			return
		}
	}

	// Create session based on queued flag
	var sess *session.Session
	var err error
	if req.Queued {
		sess, err = s.sessionManager.CreateQueued(req.AgentID)
	} else {
		sess, err = s.sessionManager.Create(req.AgentID)
	}
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create session: "+err.Error())
		return
	}

	// If an initial task is provided, add it as the first message
	if req.Task != "" {
		sess.AddUserMessage(req.Task)
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Error("Failed to save session with initial task: %v", err)
		}
	}

	providerType := config.NormalizeProviderRef(req.Provider)
	if providerType == "" {
		autoCfg := s.config.Providers[string(config.ProviderAutoRouter)]
		if s.autoRouterConfigured(autoCfg) {
			providerType = string(config.ProviderAutoRouter)
		} else {
			providerType = config.NormalizeProviderRef(s.config.ActiveProvider)
		}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = s.resolveModelForProvider(config.ProviderType(providerType))
	}
	sess.Metadata["provider"] = providerType
	sess.Metadata["model"] = model
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist session provider metadata: %v", err)
	}
	if req.ProjectID != "" {
		sess.ProjectID = &req.ProjectID
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist session project metadata: %v", err)
		}
	}
	_ = s.ensureSessionSystemPromptSnapshot(sess)
	go func(sessionID string, task string) {
		syncCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		s.syncHTTPCreatedSessionToTelegram(syncCtx, sessionID, task)
	}(sess.ID, req.Task)

	logging.LogSession("created", sess.ID, fmt.Sprintf("agent=%s via HTTP", req.AgentID))

	projectID := ""
	if sess.ProjectID != nil {
		projectID = *sess.ProjectID
	}

	s.jsonResponse(w, http.StatusCreated, CreateSessionResponse{
		ID:        sess.ID,
		AgentID:   sess.AgentID,
		ProjectID: projectID,
		Provider:  providerType,
		Model:     model,
		Status:    string(sess.Status),
		CreatedAt: sess.CreatedAt,
	})
}
func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	// Only queued sessions can be started
	if sess.Status != session.StatusQueued {
		s.errorResponse(w, http.StatusBadRequest, "Session is not in queued status")
		return
	}

	// Update status to running
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to start session: "+err.Error())
		return
	}

	logging.LogSession("started", sess.ID, fmt.Sprintf("agent=%s via HTTP", sess.AgentID))

	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}

func (s *Server) handleUpdateSessionProject(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	var req UpdateSessionProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.ProjectID == nil || strings.TrimSpace(*req.ProjectID) == "" {
		sess.ProjectID = nil
	} else {
		projectID := strings.TrimSpace(*req.ProjectID)
		if _, err := s.store.GetProject(projectID); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Project not found: "+err.Error())
			return
		}
		sess.ProjectID = &projectID
	}

	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session project: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	_ = s.ensureSessionSystemPromptSnapshot(sess)
	resp := s.sessionToResponse(sess)
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	s.cancelActiveSessionRuns(sessionID)

	sess, err := s.sessionManager.Get(sessionID)
	if err == nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cleanupCancel()
		if cleanupErr := s.deleteTelegramTopicForSession(cleanupCtx, sess); cleanupErr != nil {
			logging.Warn("Telegram topic cleanup failed for session %s: %s", sessionID, sanitizeTelegramError(cleanupErr))
		}
	}

	if err := s.sessionManager.Delete(sessionID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete session: "+err.Error())
		return
	}

	logging.LogSession("deleted", sessionID, "via HTTP")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCancelSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	cancelledRuns := s.cancelActiveSessionRuns(sessionID)
	if cancelledRuns > 0 || strings.EqualFold(string(sess.Status), string(session.StatusRunning)) {
		sess.SetStatus(session.StatusPaused)
		if saveErr := s.sessionManager.Save(sess); saveErr != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to update session status: "+saveErr.Error())
			return
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"session_id":      sessionID,
		"cancelled_runs":  cancelledRuns,
		"session_status":  string(sess.Status),
		"session_updated": sess.UpdatedAt,
	})
}

func (s *Server) handleGetPendingQuestion(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	question, err := s.sessionManager.GetPendingQuestion(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Failed to get question: "+err.Error())
		return
	}

	// Always wrap in an object for consistent API
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"question": question})
}

func (s *Server) handleGetTaskProgress(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	progress, err := s.sessionManager.GetSessionTaskProgress(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Failed to get task progress: "+err.Error())
		return
	}

	// Parse statistics from progress
	stats := parseTaskProgressStats(progress)

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"content":         progress,
		"total_tasks":     stats.Total,
		"completed_tasks": stats.Completed,
		"progress_pct":    stats.ProgressPct,
	})
}

func parseTaskProgressStats(content string) struct {
	Total       int
	Completed   int
	ProgressPct int
} {
	lines := strings.Split(content, "\n")
	total := 0
	completed := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[ ]") {
			total++
		} else if strings.HasPrefix(trimmed, "[x]") || strings.HasPrefix(trimmed, "[X]") {
			total++
			completed++
		}
	}

	pct := 0
	if total > 0 {
		pct = (completed * 100) / total
	}

	return struct {
		Total       int
		Completed   int
		ProgressPct int
	}{
		Total:       total,
		Completed:   completed,
		ProgressPct: pct,
	}
}

func (s *Server) handleAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Answer == "" {
		s.errorResponse(w, http.StatusBadRequest, "answer is required")
		return
	}

	if err := s.sessionManager.AnswerQuestion(sessionID, req.Answer); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to answer question: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

func (s *Server) registerActiveSessionRun(sessionID string, cancel context.CancelFunc) string {
	runID := uuid.New().String()
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()

	runs, ok := s.activeRuns[sessionID]
	if !ok {
		runs = make(map[string]context.CancelFunc)
		s.activeRuns[sessionID] = runs
	}
	runs[runID] = cancel
	return runID
}

func (s *Server) unregisterActiveSessionRun(sessionID, runID string) {
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()

	runs, ok := s.activeRuns[sessionID]
	if !ok {
		return
	}
	delete(runs, runID)
	if len(runs) == 0 {
		delete(s.activeRuns, sessionID)
	}
}

func (s *Server) cancelActiveSessionRuns(sessionID string) int {
	s.activeRunsMu.Lock()
	runs, ok := s.activeRuns[sessionID]
	if !ok || len(runs) == 0 {
		s.activeRunsMu.Unlock()
		return 0
	}
	cancels := make([]context.CancelFunc, 0, len(runs))
	for _, cancel := range runs {
		cancels = append(cancels, cancel)
	}
	delete(s.activeRuns, sessionID)
	s.activeRunsMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

func isCancellationError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "context canceled") || strings.Contains(lower, "cancelled")
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Message == "" {
		s.errorResponse(w, http.StatusBadRequest, "Message is required")
		return
	}

	// Get the session
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	defer s.queueTelegramSessionMessageSync(sess.ID)

	// Add user message to session
	sess.AddUserMessage(req.Message)
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session: "+err.Error())
		return
	}

	runCtx, cancelRun := context.WithCancel(r.Context())
	runID := s.registerActiveSessionRun(sessionID, cancelRun)
	defer func() {
		cancelRun()
		s.unregisterActiveSessionRun(sessionID, runID)
	}()

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	target, err := s.resolveExecutionTarget(runCtx, providerType, model, req.Message)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		s.sessionManager.Save(sess)
		s.errorResponse(w, http.StatusBadRequest, "Provider configuration error: "+err.Error())
		return
	}
	if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model) {
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist session routed target metadata: %v", err)
		}
	}

	// Create agent config
	agentConfig := agent.Config{
		Name:          sess.AgentID,
		Model:         target.Model,
		SystemPrompt:  s.buildSystemPromptForSession(sess),
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}

	// Create agent instance
	ag := agent.New(agentConfig, target.Client, s.toolManagerForSession(sess), s.sessionManager)

	// Run the agent (this is synchronous for now)
	content, usage, err := ag.Run(runCtx, sess, req.Message)
	if err != nil {
		if isCancellationError(err) {
			sess.SetStatus(session.StatusPaused)
			_ = s.sessionManager.Save(sess)
			s.errorResponse(w, http.StatusConflict, "Request was canceled before completion")
			return
		}
		sess.AddAssistantMessage(fmt.Sprintf("Request failed: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		// Save session state even on error
		s.sessionManager.Save(sess)
		s.errorResponse(w, http.StatusInternalServerError, "Agent error: "+err.Error())
		return
	}

	// Build response with updated messages
	resp := ChatResponse{
		Content:  content,
		Messages: s.messagesToResponse(sess.Messages),
		Status:   string(sess.Status),
		Usage: UsageResponse{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
		},
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Message == "" {
		s.errorResponse(w, http.StatusBadRequest, "Message is required")
		return
	}

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	defer s.queueTelegramSessionMessageSync(sess.ID)

	// Add user message before streaming begins (skip if already exists as last message).
	lastUserMsg := ""
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "user" {
			lastUserMsg = sess.Messages[i].Content
			break
		}
	}
	if lastUserMsg != req.Message {
		sess.AddUserMessage(req.Message)
	}
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session: "+err.Error())
		return
	}

	runCtx, cancelRun := context.WithCancel(r.Context())
	runID := s.registerActiveSessionRun(sessionID, cancelRun)
	defer func() {
		cancelRun()
		s.unregisterActiveSessionRun(sessionID, runID)
	}()

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	target, err := s.resolveExecutionTarget(runCtx, providerType, model, req.Message)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		s.sessionManager.Save(sess)
		s.errorResponse(w, http.StatusBadRequest, "Provider configuration error: "+err.Error())
		return
	}
	if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model) {
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist session routed target metadata: %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by the server")
		return
	}

	writeEvent := func(event ChatStreamEvent) bool {
		if err := json.NewEncoder(w).Encode(event); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeEvent(ChatStreamEvent{Type: "status", Status: string(sess.Status)}) {
		return
	}

	agentConfig := agent.Config{
		Name:          sess.AgentID,
		Model:         target.Model,
		SystemPrompt:  s.buildSystemPromptForSession(sess),
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}
	ag := agent.New(agentConfig, target.Client, s.toolManagerForSession(sess), s.sessionManager)

	content, usage, err := ag.RunWithEvents(runCtx, sess, req.Message, func(ev agent.Event) {
		switch ev.Type {
		case agent.EventAssistantDelta:
			_ = writeEvent(ChatStreamEvent{
				Type:  "assistant_delta",
				Delta: ev.Delta,
			})
		case agent.EventToolExecuting:
			toolCalls := make([]StreamToolCallEvent, len(ev.ToolCalls))
			for i, tc := range ev.ToolCalls {
				toolCalls[i] = StreamToolCallEvent{
					ID:    tc.ID,
					Name:  tc.Name,
					Input: json.RawMessage(tc.Input),
				}
			}
			_ = writeEvent(ChatStreamEvent{
				Type:      "tool_executing",
				Step:      ev.Step,
				ToolCalls: toolCalls,
			})
		case agent.EventToolCompleted:
			// Send updated messages after tool execution
			freshSess, err := s.sessionManager.Get(sess.ID)
			if err == nil {
				_ = writeEvent(ChatStreamEvent{
					Type:     "tool_completed",
					Step:     ev.Step,
					Messages: s.messagesToResponse(freshSess.Messages),
					Status:   string(freshSess.Status),
				})
			}
		case agent.EventStepCompleted:
			_ = writeEvent(ChatStreamEvent{
				Type: "step_completed",
				Step: ev.Step,
			})
		}
	})

	if err != nil {
		if isCancellationError(err) {
			sess.SetStatus(session.StatusPaused)
			s.sessionManager.Save(sess)
			_ = writeEvent(ChatStreamEvent{
				Type:   "error",
				Error:  "Request was canceled before completion.",
				Status: string(sess.Status),
			})
			return
		}
		sess.AddAssistantMessage(fmt.Sprintf("Request failed: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		s.sessionManager.Save(sess)
		_ = writeEvent(ChatStreamEvent{
			Type:   "error",
			Error:  "Agent error: " + err.Error(),
			Status: string(sess.Status),
		})
		return
	}

	_ = writeEvent(ChatStreamEvent{
		Type:     "done",
		Content:  content,
		Messages: s.messagesToResponse(sess.Messages),
		Status:   string(sess.Status),
		Usage: &UsageResponse{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
		},
	})
}

// --- Recurring Jobs Handlers ---

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list jobs: "+err.Error())
		return
	}

	resp := make([]JobResponse, len(jobs))
	for i, job := range jobs {
		resp[i] = s.jobToResponse(job)
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req CreateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return
	}
	if req.ScheduleText == "" {
		s.errorResponse(w, http.StatusBadRequest, "Schedule text is required")
		return
	}

	taskPromptSource := jobs.NormalizeTaskPromptSource(req.TaskPromptSource)
	taskPromptFile := strings.TrimSpace(req.TaskPromptFile)
	taskPrompt := strings.TrimSpace(req.TaskPrompt)
	if taskPromptSource == jobs.TaskPromptSourceFile {
		if taskPromptFile == "" {
			s.errorResponse(w, http.StatusBadRequest, "Task prompt file is required when source is file")
			return
		}
		taskPrompt = jobs.BuildTaskPromptForFile(taskPromptFile)
	} else if taskPrompt == "" {
		s.errorResponse(w, http.StatusBadRequest, "Task prompt is required")
		return
	}

	llmProvider := normalizeJobLLMProvider(req.LLMProvider)
	if llmProvider != "" {
		if err := s.validateProviderRefForExecution(llmProvider); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Unsupported LLM provider: "+llmProvider+" ("+err.Error()+")")
			return
		}
	}

	// Parse natural language schedule to cron using the agent
	cronExpr, err := s.parseScheduleToCron(r.Context(), req.ScheduleText)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to parse schedule: "+err.Error())
		return
	}

	now := time.Now()
	job := &storage.RecurringJob{
		ID:               uuid.New().String(),
		Name:             req.Name,
		ScheduleHuman:    req.ScheduleText,
		ScheduleCron:     cronExpr,
		TaskPrompt:       taskPrompt,
		TaskPromptSource: taskPromptSource,
		TaskPromptFile:   taskPromptFile,
		LLMProvider:      llmProvider,
		Enabled:          req.Enabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	// Calculate next run time
	nextRun, err := s.calculateNextRun(cronExpr, now)
	if err == nil {
		job.NextRunAt = &nextRun
	}

	if err := s.store.SaveJob(job); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save job: "+err.Error())
		return
	}

	logging.Info("Created recurring job: %s (%s)", job.Name, job.ID)
	s.jsonResponse(w, http.StatusCreated, s.jobToResponse(job))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := s.store.GetJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, s.jobToResponse(job))
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := s.store.GetJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}

	var req UpdateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	// Update fields if provided
	if req.Name != "" {
		job.Name = req.Name
	}
	if req.Enabled != nil {
		job.Enabled = *req.Enabled
	}
	if req.LLMProvider != nil {
		llmProvider := normalizeJobLLMProvider(*req.LLMProvider)
		if llmProvider != "" {
			if err := s.validateProviderRefForExecution(llmProvider); err != nil {
				s.errorResponse(w, http.StatusBadRequest, "Unsupported LLM provider: "+llmProvider+" ("+err.Error()+")")
				return
			}
		}
		job.LLMProvider = llmProvider
	}
	taskPromptSource := job.TaskPromptSource
	if req.TaskPromptSource != "" {
		taskPromptSource = jobs.NormalizeTaskPromptSource(req.TaskPromptSource)
	}
	taskPromptFile := job.TaskPromptFile
	if req.TaskPromptFile != "" {
		taskPromptFile = strings.TrimSpace(req.TaskPromptFile)
	}
	taskPrompt := job.TaskPrompt
	if req.TaskPrompt != "" {
		taskPrompt = strings.TrimSpace(req.TaskPrompt)
	}
	if taskPromptSource == jobs.TaskPromptSourceFile {
		if strings.TrimSpace(taskPromptFile) == "" {
			s.errorResponse(w, http.StatusBadRequest, "Task prompt file is required when source is file")
			return
		}
		taskPrompt = jobs.BuildTaskPromptForFile(taskPromptFile)
	} else if strings.TrimSpace(taskPrompt) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Task prompt is required")
		return
	}
	job.TaskPromptSource = taskPromptSource
	job.TaskPromptFile = strings.TrimSpace(taskPromptFile)
	job.TaskPrompt = strings.TrimSpace(taskPrompt)

	// Re-parse schedule if changed
	if req.ScheduleText != "" && req.ScheduleText != job.ScheduleHuman {
		cronExpr, err := s.parseScheduleToCron(r.Context(), req.ScheduleText)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Failed to parse schedule: "+err.Error())
			return
		}
		job.ScheduleHuman = req.ScheduleText
		job.ScheduleCron = cronExpr

		// Recalculate next run time
		nextRun, err := s.calculateNextRun(cronExpr, time.Now())
		if err == nil {
			job.NextRunAt = &nextRun
		}
	}

	job.UpdatedAt = time.Now()

	if err := s.store.SaveJob(job); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update job: "+err.Error())
		return
	}

	logging.Info("Updated recurring job: %s (%s)", job.Name, job.ID)
	s.jsonResponse(w, http.StatusOK, s.jobToResponse(job))
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	protected, err := s.isProtectedThinkingJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to check protected jobs: "+err.Error())
		return
	}
	if protected {
		s.errorResponse(w, http.StatusForbidden, "This job is managed by Thinking settings and cannot be deleted directly.")
		return
	}

	if err := s.store.DeleteJob(jobID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete job: "+err.Error())
		return
	}

	logging.Info("Deleted recurring job: %s", jobID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) isProtectedThinkingJob(jobID string) (bool, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		return false, err
	}
	thinkingJobID := strings.TrimSpace(settings[thinkingJobIDSettingKey])
	if thinkingJobID == "" {
		return false, nil
	}
	return thinkingJobID == strings.TrimSpace(jobID), nil
}

func (s *Server) handleRunJobNow(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := s.store.GetJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}

	// Execute the job immediately (in a goroutine so we don't block)
	exec, err := s.executeJob(r.Context(), job)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to execute job: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, s.executionToResponse(exec))
}

func (s *Server) handleListJobExecutions(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	// Get limit from query params, default to 20
	limit := 20
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	executions, err := s.store.ListJobExecutions(jobID, limit)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list executions: "+err.Error())
		return
	}

	resp := make([]JobExecutionResponse, len(executions))
	for i, exec := range executions {
		resp[i] = s.executionToResponse(exec)
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleListJobSessions(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	// First verify the job exists
	_, err := s.store.GetJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}

	sessions, err := s.store.ListSessionsByJob(jobID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sessions: "+err.Error())
		return
	}

	resp := make([]SessionListItem, len(sessions))
	for i, sess := range sessions {
		provider, model := storageSessionProviderAndModel(sess)
		routedProvider, routedModel := storageSessionRoutedProviderAndModel(sess)
		projectID := ""
		if sess.ProjectID != nil {
			projectID = *sess.ProjectID
		}
		resp[i] = SessionListItem{
			ID:                 sess.ID,
			AgentID:            sess.AgentID,
			ProjectID:          projectID,
			Provider:           provider,
			Model:              model,
			RoutedProvider:     routedProvider,
			RoutedModel:        routedModel,
			Title:              sess.Title,
			Status:             sess.Status,
			TotalTokens:        storageSessionTotalTokens(sess),
			RunDurationSeconds: sessionRunDurationSeconds(sess.CreatedAt, sess.UpdatedAt, sess.Status),
			TaskProgress:       sess.TaskProgress,
			CreatedAt:          sess.CreatedAt,
			UpdatedAt:          sess.UpdatedAt,
		}
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

// parseScheduleToCron uses the LLM to convert natural language schedule to cron expression
func (s *Server) parseScheduleToCron(ctx context.Context, scheduleText string) (string, error) {
	prompt := fmt.Sprintf(`Convert the following natural language schedule to a standard 5-field cron expression.
Only respond with the cron expression, nothing else. No explanation, no formatting, just the cron expression.

Schedule: "%s"

Examples:
- "every day at 7pm" -> "0 19 * * *"
- "every Monday at 9am" -> "0 9 * * 1"
- "every hour" -> "0 * * * *"
- "every weekday at 8:30am" -> "30 8 * * 1-5"
- "every 15 minutes" -> "*/15 * * * *"

Cron expression:`, scheduleText)

	// Create a minimal session for this parsing task
	sess, err := s.sessionManager.Create("scheduler")
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer s.sessionManager.Delete(sess.ID)

	sess.AddUserMessage(prompt)

	providerType := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
	model := s.resolveModelForProvider(providerType)
	target, err := s.resolveExecutionTarget(ctx, providerType, model, prompt)
	if err != nil {
		return "", fmt.Errorf("failed to initialize provider %s: %w", providerType, err)
	}

	// Create agent config for parsing
	agentConfig := agent.Config{
		Name:          "scheduler",
		Model:         target.Model,
		SystemPrompt:  "You convert natural-language schedules into strict 5-field cron expressions.",
		MaxSteps:      1, // Only need one response
		Temperature:   0, // Deterministic output
		ContextWindow: target.ContextWindow,
	}

	ag := agent.New(agentConfig, target.Client, s.toolManagerForSession(sess), s.sessionManager)
	cronExpr, _, err := ag.Run(ctx, sess, prompt)
	if err != nil {
		return "", fmt.Errorf("failed to parse schedule: %w", err)
	}

	// Clean up the response (trim whitespace)
	cronExpr = strings.TrimSpace(cronExpr)

	// Basic validation: should have 5 fields
	fields := strings.Fields(cronExpr)
	if len(fields) != 5 {
		return "", fmt.Errorf("invalid cron expression: %s", cronExpr)
	}

	return cronExpr, nil
}

// calculateNextRun calculates the next run time based on cron expression
func (s *Server) calculateNextRun(cronExpr string, after time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	return schedule.Next(after), nil
}

// executeJob runs a job and returns the execution record
func (s *Server) executeJob(ctx context.Context, job *storage.RecurringJob) (*storage.JobExecution, error) {
	now := time.Now()

	// Create execution record
	exec := &storage.JobExecution{
		ID:        uuid.New().String(),
		JobID:     job.ID,
		Status:    "running",
		StartedAt: now,
	}

	if err := s.store.SaveJobExecution(exec); err != nil {
		return nil, fmt.Errorf("failed to create execution record: %w", err)
	}

	// Create a session for this job execution
	sess, err := s.sessionManager.CreateWithJob("job-runner", job.ID)
	if err != nil {
		exec.Status = "failed"
		exec.Error = "Failed to create session: " + err.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return exec, nil
	}
	isThinkingJob := false
	if thinking, thinkErr := s.isProtectedThinkingJob(job.ID); thinkErr != nil {
		logging.Warn("Failed to check thinking job for project assignment: %v", thinkErr)
	} else if thinking {
		isThinkingJob = true
		if assignErr := s.assignSessionToThinkingProject(sess); assignErr != nil {
			logging.Warn("Failed to assign Thinking project for session %s: %v", sess.ID, assignErr)
		}
	}

	exec.SessionID = sess.ID

	providerType := s.resolveJobProviderType(job)
	model := s.resolveModelForProvider(providerType)
	sess.Metadata["provider"] = string(providerType)
	sess.Metadata["model"] = model
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist job session provider metadata: %v", err)
	}
	_ = s.ensureSessionSystemPromptSnapshot(sess)

	effectiveTaskPrompt, resolveErr := jobs.ResolveTaskPrompt(job)
	if resolveErr != nil {
		exec.Status = "failed"
		exec.Error = "Failed to resolve task instructions: " + resolveErr.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return exec, nil
	}
	if isThinkingJob {
		effectiveTaskPrompt = thinkingRunTaskPrompt
	}

	target, clientErr := s.resolveExecutionTarget(ctx, providerType, model, effectiveTaskPrompt)
	if clientErr != nil {
		exec.Status = "failed"
		exec.Error = "Failed to initialize provider: " + clientErr.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return exec, nil
	}
	if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model) {
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist job session routed target metadata: %v", err)
		}
	}

	// Run the agent with resolved task prompt
	agentConfig := agent.Config{
		Name:          "job-runner",
		Model:         target.Model,
		SystemPrompt:  s.buildSystemPromptForSession(sess),
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}
	ag := agent.New(agentConfig, target.Client, s.toolManagerForSession(sess), s.sessionManager)
	sess.AddUserMessage(effectiveTaskPrompt)
	output, _, err := ag.Run(ctx, sess, effectiveTaskPrompt)

	finishedAt := time.Now()
	exec.FinishedAt = &finishedAt

	if err != nil {
		exec.Status = "failed"
		exec.Error = err.Error()
	} else {
		exec.Status = "success"
		exec.Output = output
	}

	// Update execution record
	if err := s.store.SaveJobExecution(exec); err != nil {
		logging.Error("Failed to update execution record: %v", err)
	}

	// Update job's last run time and calculate next run
	job.LastRunAt = &now
	nextRun, err := s.calculateNextRun(job.ScheduleCron, now)
	if err == nil {
		job.NextRunAt = &nextRun
	}
	job.UpdatedAt = now

	if err := s.store.SaveJob(job); err != nil {
		logging.Error("Failed to update job after execution: %v", err)
	}

	return exec, nil
}

func (s *Server) assignSessionToThinkingProject(sess *session.Session) error {
	now := time.Now()
	project := &storage.Project{
		ID:        thinkingProjectID,
		Name:      thinkingProjectName,
		IsSystem:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.SaveProject(project); err != nil {
		return err
	}
	sess.ProjectID = &project.ID
	return s.sessionManager.Save(sess)
}

// jobToResponse converts a storage job to API response
func (s *Server) jobToResponse(job *storage.RecurringJob) JobResponse {
	return JobResponse{
		ID:               job.ID,
		Name:             job.Name,
		ScheduleHuman:    job.ScheduleHuman,
		ScheduleCron:     job.ScheduleCron,
		TaskPrompt:       job.TaskPrompt,
		TaskPromptSource: jobs.NormalizeTaskPromptSource(job.TaskPromptSource),
		TaskPromptFile:   strings.TrimSpace(job.TaskPromptFile),
		LLMProvider:      job.LLMProvider,
		Enabled:          job.Enabled,
		LastRunAt:        job.LastRunAt,
		NextRunAt:        job.NextRunAt,
		CreatedAt:        job.CreatedAt,
		UpdatedAt:        job.UpdatedAt,
	}
}

// executionToResponse converts a storage execution to API response
func (s *Server) executionToResponse(exec *storage.JobExecution) JobExecutionResponse {
	return JobExecutionResponse{
		ID:         exec.ID,
		JobID:      exec.JobID,
		SessionID:  exec.SessionID,
		Status:     exec.Status,
		Output:     exec.Output,
		Error:      exec.Error,
		StartedAt:  exec.StartedAt,
		FinishedAt: exec.FinishedAt,
	}
}

// --- Helper methods ---

func (s *Server) sessionToResponse(sess *session.Session) SessionResponse {
	parentID := ""
	if sess.ParentID != nil {
		parentID = *sess.ParentID
	}
	projectID := ""
	if sess.ProjectID != nil {
		projectID = *sess.ProjectID
	}
	provider, model := sessionProviderAndModel(sess)
	routedProvider, routedModel := sessionRoutedProviderAndModel(sess)
	snapshot := sessionSystemPromptSnapshot(sess)
	var snapshotPayload *SystemPromptSnapshotPayload
	if snapshot != nil {
		blocks := make([]SystemPromptBlockSnapshotPayload, len(snapshot.Blocks))
		for i, block := range snapshot.Blocks {
			blocks[i] = SystemPromptBlockSnapshotPayload{
				Type:            block.Type,
				Value:           block.Value,
				Enabled:         block.Enabled,
				ResolvedContent: block.ResolvedContent,
				SourcePath:      block.SourcePath,
				Error:           block.Error,
				EstimatedTokens: block.EstimatedTokens,
			}
		}
		snapshotPayload = &SystemPromptSnapshotPayload{
			BasePrompt:        snapshot.BasePrompt,
			CombinedPrompt:    snapshot.CombinedPrompt,
			BaseEstimated:     snapshot.BaseEstimated,
			CombinedEstimated: snapshot.CombinedEstimated,
			Blocks:            blocks,
		}
	}
	inputTokens, outputTokens := sessionInputOutputTokens(sess)
	totalTokens := inputTokens + outputTokens
	currentContextTokens := int(metadataNumber(sess.Metadata, "current_context_tokens"))
	modelContextWindow := int(metadataNumber(sess.Metadata, "context_window"))

	return SessionResponse{
		ID:                   sess.ID,
		AgentID:              sess.AgentID,
		ParentID:             parentID,
		ProjectID:            projectID,
		Provider:             provider,
		Model:                model,
		RoutedProvider:       routedProvider,
		RoutedModel:          routedModel,
		Title:                sess.Title,
		Status:               string(sess.Status),
		TotalTokens:          totalTokens,
		InputTokens:          inputTokens,
		OutputTokens:         outputTokens,
		CurrentContextTokens: currentContextTokens,
		ModelContextWindow:   modelContextWindow,
		TaskProgress:         sess.TaskProgress,
		CreatedAt:            sess.CreatedAt,
		UpdatedAt:            sess.UpdatedAt,
		Messages:             s.messagesToResponse(sess.Messages),
		SystemPromptSnapshot: snapshotPayload,
	}
}

func (s *Server) messagesToResponse(messages []session.Message) []MessageResponse {
	resp := make([]MessageResponse, len(messages))
	for i, m := range messages {
		msg := MessageResponse{
			Role:      m.Role,
			Content:   m.Content,
			Metadata:  m.Metadata,
			Timestamp: m.Timestamp,
		}

		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]ToolCallResponse, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				msg.ToolCalls[j] = ToolCallResponse{
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Input,
				}
			}
		}

		if len(m.ToolResults) > 0 {
			msg.ToolResults = make([]ToolResultResponse, len(m.ToolResults))
			for j, tr := range m.ToolResults {
				msg.ToolResults[j] = ToolResultResponse{
					ToolCallID: tr.ToolCallID,
					Content:    tr.Content,
					IsError:    tr.IsError,
					Metadata:   tr.Metadata,
					Name:       tr.Name,
				}
			}
		}

		resp[i] = msg
	}
	return resp
}

func (s *Server) jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (s *Server) errorResponse(w http.ResponseWriter, status int, message string) {
	logging.Error("HTTP error: %d - %s", status, message)
	s.jsonResponse(w, status, map[string]string{"error": message})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list projects: "+err.Error())
		return
	}

	resp := make([]ProjectResponse, len(projects))
	for i, project := range projects {
		resp[i] = projectToResponse(project)
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project name is required")
		return
	}

	now := time.Now()
	project := &storage.Project{
		ID:        uuid.New().String(),
		Name:      name,
		Folder:    normalizeFolder(req.Folder),
		IsSystem:  false,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.store.SaveProject(project); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save project: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusCreated, projectToResponse(project))
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, projectToResponse(project))
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found: "+err.Error())
		return
	}

	var req UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			s.errorResponse(w, http.StatusBadRequest, "Project name cannot be empty")
			return
		}
		project.Name = name
	}
	if req.Folder != nil {
		project.Folder = normalizeFolder(req.Folder)
	}
	project.UpdatedAt = time.Now()

	if err := s.store.SaveProject(project); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update project: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, projectToResponse(project))
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	if err := s.store.DeleteProject(projectID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete project: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type configuredInstructionBlock struct {
	Type    string `json:"type"`
	Value   string `json:"value"`
	Enabled *bool  `json:"enabled,omitempty"`
}

type systemPromptSnapshot struct {
	BasePrompt        string                      `json:"base_prompt"`
	CombinedPrompt    string                      `json:"combined_prompt"`
	BaseEstimated     int                         `json:"base_estimated_tokens"`
	CombinedEstimated int                         `json:"combined_estimated_tokens"`
	Blocks            []systemPromptBlockSnapshot `json:"blocks"`
}

type systemPromptBlockSnapshot struct {
	Type            string `json:"type"`
	Value           string `json:"value"`
	Enabled         bool   `json:"enabled"`
	ResolvedContent string `json:"resolved_content,omitempty"`
	SourcePath      string `json:"source_path,omitempty"`
	Error           string `json:"error,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

func (s *Server) buildSystemPromptForSession(sess *session.Session) string {
	snapshot := s.ensureSessionSystemPromptSnapshot(sess)
	if snapshot == nil {
		return ""
	}
	return strings.TrimSpace(snapshot.CombinedPrompt)
}

func (s *Server) ensureSessionSystemPromptSnapshot(sess *session.Session) *systemPromptSnapshot {
	if sess == nil {
		return nil
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to load settings for system prompt composition: %v", err)
		settings = map[string]string{}
	}
	thinkingBlocks := resolveThinkingInstructionBlocksFromSettings(settings)

	if snapshot := sessionSystemPromptSnapshot(sess); snapshot != nil {
		if !isThinkingSessionWithSettings(sess, settings) || len(thinkingBlocks) == 0 || snapshotHasThinkingBlocks(snapshot) {
			return snapshot
		}
	}

	snapshot := s.composeSystemPromptSnapshotWithSettings(sess, settings)
	if snapshot == nil {
		return nil
	}
	attachSessionSystemPromptSnapshot(sess, snapshot)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist system prompt snapshot for session %s: %v", sess.ID, err)
	}
	return snapshot
}

func sessionSystemPromptSnapshot(sess *session.Session) *systemPromptSnapshot {
	if sess == nil || sess.Metadata == nil {
		return nil
	}
	raw, ok := sess.Metadata[sessionSystemPromptSnapshotMetadataKey]
	if !ok {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot systemPromptSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil
	}
	snapshot.CombinedPrompt = strings.TrimSpace(snapshot.CombinedPrompt)
	snapshot.BasePrompt = strings.TrimSpace(snapshot.BasePrompt)
	if snapshot.CombinedPrompt == "" {
		return nil
	}
	return &snapshot
}

func attachSessionSystemPromptSnapshot(sess *session.Session, snapshot *systemPromptSnapshot) {
	if sess == nil || snapshot == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	sess.Metadata[sessionSystemPromptSnapshotMetadataKey] = snapshot
}

func (s *Server) composeSystemPromptSnapshot(sess *session.Session) *systemPromptSnapshot {
	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to load settings for system prompt composition: %v", err)
		settings = map[string]string{}
	}
	return s.composeSystemPromptSnapshotWithSettings(sess, settings)
}

func (s *Server) resolveProjectContextSection(sess *session.Session) (string, int) {
	if sess == nil || sess.ProjectID == nil {
		return "", 0
	}
	projectID := strings.TrimSpace(*sess.ProjectID)
	if projectID == "" {
		return "", 0
	}
	project, err := s.store.GetProject(projectID)
	if err != nil || project == nil {
		return "", 0
	}
	if project.Folder == nil || *project.Folder == "" {
		return "", 0
	}
	var sb strings.Builder
	sb.WriteString("Project context:\n")
	sb.WriteString(fmt.Sprintf("- Project: %s\n", project.Name))
	sb.WriteString("- Associated folder:\n")
	sb.WriteString(fmt.Sprintf("  - %s\n", *project.Folder))
	sb.WriteString("\nWhen working on this project, prefer operating within the associated folder above.")
	content := sb.String()
	return content, estimateTokensApprox(content)
}

func (s *Server) composeSystemPromptSnapshotWithSettings(sess *session.Session, settings map[string]string) *systemPromptSnapshot {
	rawBlocks := strings.TrimSpace(settings[agentInstructionBlocksSettingKey])
	blocks := []configuredInstructionBlock{}
	if rawBlocks != "" {
		if err := json.Unmarshal([]byte(rawBlocks), &blocks); err != nil {
			logging.Warn("Failed to parse %s: %v", agentInstructionBlocksSettingKey, err)
			blocks = []configuredInstructionBlock{}
		}
	}
	hasBuiltInBlock := false
	hasIntegrationBlock := false
	hasExternalMarkdownBlock := false
	hasMCPServersBlock := false
	for _, block := range blocks {
		switch strings.TrimSpace(block.Type) {
		case builtInToolsInstructionBlockType:
			hasBuiltInBlock = true
		case integrationSkillsInstructionBlockType:
			hasIntegrationBlock = true
		case externalMarkdownSkillsInstructionBlockType:
			hasExternalMarkdownBlock = true
		case mcpServersInstructionBlockType:
			hasMCPServersBlock = true
		}
	}
	prefixedBlocks := make([]configuredInstructionBlock, 0, 4+len(blocks))
	if !hasBuiltInBlock {
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: builtInToolsInstructionBlockType, Value: ""})
	}
	if !hasIntegrationBlock {
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: integrationSkillsInstructionBlockType, Value: ""})
	}
	if !hasExternalMarkdownBlock {
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: externalMarkdownSkillsInstructionBlockType, Value: ""})
	}
	if !hasMCPServersBlock {
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: mcpServersInstructionBlockType, Value: ""})
	}
	blocks = append(prefixedBlocks, blocks...)

	builtInToolsEnabled := true
	for _, block := range blocks {
		if strings.TrimSpace(block.Type) != builtInToolsInstructionBlockType {
			continue
		}
		builtInToolsEnabled = block.Enabled == nil || *block.Enabled
		break
	}

	basePrompt := strings.TrimSpace(os.Getenv("AAGENT_SYSTEM_PROMPT"))
	if basePrompt == "" {
		if builtInToolsEnabled {
			basePrompt = agent.DefaultSystemPrompt()
		} else {
			basePrompt = agent.DefaultSystemPromptWithoutBuiltInTools()
		}
	}
	if basePrompt == "" {
		return nil
	}

	resolvedBlocks := make([]systemPromptBlockSnapshot, 0, len(blocks))
	resolvedBlocks = append(resolvedBlocks, systemPromptBlockSnapshot{
		Type:            builtInToolsInstructionBlockType,
		Value:           "",
		Enabled:         builtInToolsEnabled,
		ResolvedContent: "Controls whether built-in tool guidance is included in the base system prompt.",
		EstimatedTokens: builtInToolsEstimatedTokens(basePrompt, builtInToolsEnabled),
	})
	appendSections := make([]string, 0, len(blocks))
	sectionNumber := 0
	for _, block := range blocks {
		blockType := strings.TrimSpace(block.Type)
		if blockType == builtInToolsInstructionBlockType {
			continue
		}
		sectionNumber++

		enabled := block.Enabled == nil || *block.Enabled
		blockSnapshot := systemPromptBlockSnapshot{
			Type:    blockType,
			Value:   strings.TrimSpace(block.Value),
			Enabled: enabled,
		}
		if !enabled {
			resolvedBlocks = append(resolvedBlocks, blockSnapshot)
			continue
		}

		value := blockSnapshot.Value
		switch blockType {
		case integrationSkillsInstructionBlockType:
			section, resolveErr := s.resolveIntegrationSkillsSection(sectionNumber)
			blockSnapshot.ResolvedContent = section
			blockSnapshot.Error = resolveErr
			if section == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.EstimatedTokens = estimateTokensApprox(section)
			appendSections = append(appendSections, section)
		case externalMarkdownSkillsInstructionBlockType:
			section, estimatedTokens, resolveErr := s.resolveExternalMarkdownSkillsSection(settings, sectionNumber)
			blockSnapshot.ResolvedContent = section
			blockSnapshot.Error = resolveErr
			if section == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.EstimatedTokens = estimatedTokens
			appendSections = append(appendSections, section)
		case mcpServersInstructionBlockType:
			section, estimatedTokens, resolveErr := s.resolveMCPServersSection(sectionNumber)
			blockSnapshot.ResolvedContent = section
			blockSnapshot.Error = resolveErr
			if section == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.EstimatedTokens = estimatedTokens
			appendSections = append(appendSections, section)
		case "text":
			if value == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.ResolvedContent = value
			rendered := fmt.Sprintf("Instruction block %d (text):\n%s", sectionNumber, blockSnapshot.ResolvedContent)
			blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
			appendSections = append(appendSections, rendered)
		case "file":
			if value == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.SourcePath = value
			content, readErr := s.readInstructionFileBlock(value)
			if readErr != nil {
				blockSnapshot.Error = readErr.Error()
				rendered := fmt.Sprintf("Instruction block %d (file):\nUnable to load file %s: %s", sectionNumber, value, readErr.Error())
				blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
				appendSections = append(appendSections, rendered)
			} else {
				blockSnapshot.ResolvedContent = content
				rendered := fmt.Sprintf("Instruction block %d (file):\n%s", sectionNumber, content)
				blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
				appendSections = append(appendSections, rendered)
			}
		case "project_agents_md":
			section := s.resolveProjectAgentsMDSection(sess, settings, value, sectionNumber)
			blockSnapshot.ResolvedContent = section
			if section == "" {
				blockSnapshot.Error = "No project/My Mind instruction file content found."
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.EstimatedTokens = estimateTokensApprox(section)
			appendSections = append(appendSections, section)
		default:
			if value == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.Type = "text"
			blockSnapshot.ResolvedContent = value
			rendered := fmt.Sprintf("Instruction block %d (text):\n%s", sectionNumber, value)
			blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
			appendSections = append(appendSections, rendered)
		}
		resolvedBlocks = append(resolvedBlocks, blockSnapshot)
	}
	if isThinkingSessionWithSettings(sess, settings) {
		thinkingBlocks := resolveThinkingInstructionBlocksFromSettings(settings)
		for _, block := range thinkingBlocks {
			blockType := strings.TrimSpace(block.Type)
			enabled := block.Enabled == nil || *block.Enabled
			blockSnapshot := systemPromptBlockSnapshot{
				Type:    "thinking_" + blockType,
				Value:   strings.TrimSpace(block.Value),
				Enabled: enabled,
			}
			sectionNumber++
			if !enabled {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}

			value := blockSnapshot.Value
			switch blockType {
			case "text":
				blockSnapshot.ResolvedContent = value
				rendered := fmt.Sprintf("Thinking instruction block %d (text):\n%s", sectionNumber, value)
				blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
				appendSections = append(appendSections, rendered)
			case "file":
				blockSnapshot.SourcePath = value
				content, readErr := s.readInstructionFileBlock(value)
				if readErr != nil {
					blockSnapshot.Error = readErr.Error()
					rendered := fmt.Sprintf("Thinking instruction block %d (file):\nUnable to load file %s: %s", sectionNumber, value, readErr.Error())
					blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
					appendSections = append(appendSections, rendered)
				} else {
					blockSnapshot.ResolvedContent = content
					rendered := fmt.Sprintf("Thinking instruction block %d (file):\n%s", sectionNumber, content)
					blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
					appendSections = append(appendSections, rendered)
				}
			case "project_agents_md":
				section := s.resolveProjectAgentsMDSection(sess, settings, value, sectionNumber)
				blockSnapshot.ResolvedContent = section
				if section == "" {
					blockSnapshot.Error = "No project/My Mind instruction file content found."
					resolvedBlocks = append(resolvedBlocks, blockSnapshot)
					continue
				}
				blockSnapshot.EstimatedTokens = estimateTokensApprox(section)
				appendSections = append(appendSections, section)
			default:
				if value == "" {
					resolvedBlocks = append(resolvedBlocks, blockSnapshot)
					continue
				}
				blockSnapshot.Type = "thinking_text"
				blockSnapshot.ResolvedContent = value
				rendered := fmt.Sprintf("Thinking instruction block %d (text):\n%s", sectionNumber, value)
				blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
				appendSections = append(appendSections, rendered)
			}
			resolvedBlocks = append(resolvedBlocks, blockSnapshot)
		}
	}

	// Inject project context as the first section if a project is associated
	projectContext, projectContextTokens := s.resolveProjectContextSection(sess)
	if projectContext != "" {
		appendSections = append([]string{projectContext}, appendSections...)
		resolvedBlocks = append([]systemPromptBlockSnapshot{{
			Type:            "project_context",
			Value:           "",
			Enabled:         true,
			ResolvedContent: projectContext,
			EstimatedTokens: projectContextTokens,
		}}, resolvedBlocks...)
	}

	if len(appendSections) == 0 {
		if appendPrompt := strings.TrimSpace(os.Getenv("AAGENT_SYSTEM_PROMPT_APPEND")); appendPrompt != "" {
			combinedPrompt := strings.TrimSpace(basePrompt) + "\n\n" + appendPrompt
			return &systemPromptSnapshot{
				BasePrompt:        basePrompt,
				CombinedPrompt:    combinedPrompt,
				BaseEstimated:     estimateTokensApprox(basePrompt),
				CombinedEstimated: estimateTokensApprox(combinedPrompt),
				Blocks:            resolvedBlocks,
			}
		}
		return &systemPromptSnapshot{
			BasePrompt:        basePrompt,
			CombinedPrompt:    basePrompt,
			BaseEstimated:     estimateTokensApprox(basePrompt),
			CombinedEstimated: estimateTokensApprox(basePrompt),
			Blocks:            resolvedBlocks,
		}
	}
	combinedPrompt := strings.TrimSpace(basePrompt) + "\n\nApply these additional instructions in order:\n\n" + strings.Join(appendSections, "\n\n")
	return &systemPromptSnapshot{
		BasePrompt:        basePrompt,
		CombinedPrompt:    combinedPrompt,
		BaseEstimated:     estimateTokensApprox(basePrompt),
		CombinedEstimated: estimateTokensApprox(combinedPrompt),
		Blocks:            resolvedBlocks,
	}
}

func resolveThinkingInstructionBlocksFromSettings(settings map[string]string) []configuredInstructionBlock {
	if settings == nil {
		return []configuredInstructionBlock{}
	}

	rawBlocks := strings.TrimSpace(settings[thinkingInstructionBlocksSettingKey])
	if rawBlocks != "" {
		parsed := []configuredInstructionBlock{}
		if err := json.Unmarshal([]byte(rawBlocks), &parsed); err != nil {
			logging.Warn("Failed to parse %s: %v", thinkingInstructionBlocksSettingKey, err)
			return []configuredInstructionBlock{}
		}
		normalized := make([]configuredInstructionBlock, 0, len(parsed))
		for _, block := range parsed {
			blockType := strings.TrimSpace(block.Type)
			value := strings.TrimSpace(block.Value)
			enabled := block.Enabled == nil || *block.Enabled
			if !enabled {
				continue
			}
			if blockType == "project_agents_md" || value != "" {
				enabledCopy := true
				normalized = append(normalized, configuredInstructionBlock{
					Type:    blockType,
					Value:   value,
					Enabled: &enabledCopy,
				})
			}
		}
		return normalized
	}

	source := strings.TrimSpace(strings.ToLower(settings[thinkingSourceSettingKey]))
	textValue := strings.TrimSpace(settings[thinkingTextSettingKey])
	fileValue := strings.TrimSpace(settings[thinkingFilePathSettingKey])
	switch source {
	case "file":
		if fileValue != "" {
			enabled := true
			return []configuredInstructionBlock{{Type: "file", Value: fileValue, Enabled: &enabled}}
		}
	case "text":
		if textValue != "" {
			enabled := true
			return []configuredInstructionBlock{{Type: "text", Value: textValue, Enabled: &enabled}}
		}
	}

	if fileValue != "" {
		enabled := true
		return []configuredInstructionBlock{{Type: "file", Value: fileValue, Enabled: &enabled}}
	}
	if textValue != "" {
		enabled := true
		return []configuredInstructionBlock{{Type: "text", Value: textValue, Enabled: &enabled}}
	}
	return []configuredInstructionBlock{}
}

func isThinkingSessionWithSettings(sess *session.Session, settings map[string]string) bool {
	if sess == nil {
		return false
	}
	if sess.ProjectID != nil && strings.TrimSpace(*sess.ProjectID) == thinkingProjectID {
		return true
	}
	if sess.JobID == nil {
		return false
	}
	thinkingJobID := strings.TrimSpace(settings[thinkingJobIDSettingKey])
	if thinkingJobID == "" {
		return false
	}
	return strings.TrimSpace(*sess.JobID) == thinkingJobID
}

func snapshotHasThinkingBlocks(snapshot *systemPromptSnapshot) bool {
	if snapshot == nil {
		return false
	}
	for _, block := range snapshot.Blocks {
		if strings.HasPrefix(strings.TrimSpace(block.Type), "thinking_") {
			return true
		}
	}
	return false
}

func builtInToolsEstimatedTokens(basePrompt string, enabled bool) int {
	if !enabled {
		return 0
	}
	if strings.TrimSpace(os.Getenv("AAGENT_SYSTEM_PROMPT")) != "" {
		return 0
	}
	withTools := estimateTokensApprox(agent.DefaultSystemPrompt())
	withoutTools := estimateTokensApprox(agent.DefaultSystemPromptWithoutBuiltInTools())
	diff := withTools - withoutTools
	if diff < 0 {
		return 0
	}
	_ = basePrompt
	return diff
}

func estimateTokensApprox(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runes := utf8.RuneCountInString(trimmed)
	if runes <= 0 {
		return 0
	}
	return int(math.Ceil(float64(runes) / 4.0))
}

func (s *Server) readInstructionFileBlock(path string) (string, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return "", fmt.Errorf("empty file path")
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("file is empty")
	}
	if len(content) > maxDynamicInstructionBytes {
		content = content[:maxDynamicInstructionBytes] + "\n\n[truncated]"
	}
	return content, nil
}

func (s *Server) resolveProjectAgentsMDSection(sess *session.Session, settings map[string]string, rawFilename string, blockNumber int) string {
	folders := make([]string, 0, 4)
	if sess != nil && sess.ProjectID != nil && strings.TrimSpace(*sess.ProjectID) != "" {
		project, err := s.store.GetProject(strings.TrimSpace(*sess.ProjectID))
		if err == nil && project != nil && project.Folder != nil {
			folders = append(folders, *project.Folder)
		}
	}
	if len(folders) == 0 {
		mindRoot := strings.TrimSpace(settings[mindRootFolderSettingKey])
		if mindRoot != "" {
			folders = append(folders, mindRoot)
		}
	}
	if len(folders) == 0 {
		return ""
	}

	filename := strings.TrimSpace(rawFilename)
	if filename == "" {
		filename = defaultDynamicInstructionFile
	}

	pathsToTry := []string{filename}
	lower := strings.ToLower(filename)
	if lower != filename {
		pathsToTry = append(pathsToTry, lower)
	}

	collected := make([]string, 0, len(folders))
	for _, folder := range folders {
		base := strings.TrimSpace(folder)
		if base == "" {
			continue
		}
		for _, rel := range pathsToTry {
			candidate := rel
			if !filepath.IsAbs(rel) {
				candidate = filepath.Join(base, rel)
			}
			data, readErr := os.ReadFile(candidate)
			if readErr != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			if len(content) > maxDynamicInstructionBytes {
				content = content[:maxDynamicInstructionBytes] + "\n\n[truncated]"
			}
			collected = append(collected, fmt.Sprintf("Project instruction file (%s):\n%s", candidate, content))
			break
		}
	}

	if len(collected) == 0 {
		return ""
	}

	return fmt.Sprintf("Instruction block %d (dynamic project file):\n%s", blockNumber, strings.Join(collected, "\n\n"))
}

func (s *Server) resolveIntegrationSkillsSection(blockNumber int) (string, string) {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		return "", "Failed to list integrations: " + err.Error()
	}

	type skillEntry struct {
		name     string
		provider string
		mode     string
	}
	entries := make([]skillEntry, 0, len(integrations))
	for _, integration := range integrations {
		if integration == nil || !integration.Enabled {
			continue
		}
		entries = append(entries, skillEntry{
			name:     strings.TrimSpace(integration.Name),
			provider: strings.TrimSpace(integration.Provider),
			mode:     strings.TrimSpace(integration.Mode),
		})
	}

	if len(entries) == 0 {
		return "", "No enabled integrations are configured."
	}

	sort.Slice(entries, func(i, j int) bool {
		left := strings.ToLower(entries[i].provider + "|" + entries[i].name + "|" + entries[i].mode)
		right := strings.ToLower(entries[j].provider + "|" + entries[j].name + "|" + entries[j].mode)
		return left < right
	})

	lines := make([]string, 0, len(entries)+2)
	lines = append(lines, fmt.Sprintf("Instruction block %d (integration-backed skills):", blockNumber))
	lines = append(lines, "Enabled integrations available to the agent (integration mode controls channel behavior, not tool availability):")
	for _, entry := range entries {
		label := entry.name
		if label == "" {
			label = entry.provider
		}
		mode := entry.mode
		if mode == "" {
			mode = "unknown"
		}
		lines = append(lines, fmt.Sprintf("- %s (%s/%s)", label, entry.provider, mode))
	}

	return strings.Join(lines, "\n"), ""
}

func (s *Server) resolveMCPServersSection(blockNumber int) (string, int, string) {
	servers, err := s.store.ListMCPServers()
	if err != nil {
		return "", 0, "Failed to list MCP servers: " + err.Error()
	}

	type mcpEntry struct {
		name      string
		transport string
		tools     int
		tokens    int
	}
	entries := make([]mcpEntry, 0, len(servers))
	totalTokens := 0
	for _, server := range servers {
		if server == nil || !server.Enabled {
			continue
		}

		tokenEstimate := 0
		if server.LastEstimatedTokens != nil && *server.LastEstimatedTokens > 0 {
			tokenEstimate = *server.LastEstimatedTokens
		}
		toolCount := 0
		if server.LastToolCount != nil && *server.LastToolCount > 0 {
			toolCount = *server.LastToolCount
		}
		totalTokens += tokenEstimate
		entries = append(entries, mcpEntry{
			name:      strings.TrimSpace(server.Name),
			transport: strings.TrimSpace(server.Transport),
			tools:     toolCount,
			tokens:    tokenEstimate,
		})
	}

	if len(entries) == 0 {
		return "", 0, "No enabled MCP servers are configured."
	}

	sort.Slice(entries, func(i, j int) bool {
		left := strings.ToLower(entries[i].name + "|" + entries[i].transport)
		right := strings.ToLower(entries[j].name + "|" + entries[j].transport)
		return left < right
	})

	lines := make([]string, 0, len(entries)+4)
	lines = append(lines, fmt.Sprintf("Instruction block %d (MCP servers):", blockNumber))
	lines = append(lines, "Enabled MCP servers available to the agent. Manage these in MCP section: /mcp")
	for _, entry := range entries {
		label := entry.name
		if label == "" {
			label = "Unnamed MCP server"
		}
		transport := entry.transport
		if transport == "" {
			transport = "unknown"
		}
		lines = append(lines, fmt.Sprintf("- %s (%s, %d tools, %d tokens)", label, transport, entry.tools, entry.tokens))
	}
	lines = append(lines, fmt.Sprintf("Total MCP servers estimated tokens: %d", totalTokens))

	return strings.Join(lines, "\n"), totalTokens, ""
}

func (s *Server) resolveExternalMarkdownSkillsSection(settings map[string]string, blockNumber int) (string, int, string) {
	folder := strings.TrimSpace(settings[skillsFolderSettingKey])
	if folder == "" {
		return "", 0, "Skills folder is not configured."
	}

	resolvedFolder, err := filepath.Abs(folder)
	if err != nil {
		return "", 0, "Invalid skills folder path."
	}

	info, err := os.Stat(resolvedFolder)
	if err != nil {
		return "", 0, "Skills folder is not accessible: " + err.Error()
	}
	if !info.IsDir() {
		return "", 0, "Skills folder path is not a directory."
	}

	// Load configuration
	config, configErr := skillsLoader.LoadConfig(resolvedFolder)
	if configErr != nil {
		// Use default config on error
		config = skillsLoader.DefaultConfig()
	}

	// Load all skills with configuration
	allSkills, loadErr := skillsLoader.LoadSkillsFromDirectory(resolvedFolder, config)
	if loadErr != nil {
		return "", 0, "Failed to load skills: " + loadErr.Error()
	}

	if len(allSkills) == 0 {
		return "", 0, "No markdown skills discovered in configured skills folder."
	}

	// Separate skills by strategy
	alwaysSkills := make([]*skillsLoader.Skill, 0)
	onDemandSkills := make([]*skillsLoader.Skill, 0)

	for _, skill := range allSkills {
		if skill.Strategy == skillsLoader.StrategyDisabled {
			continue
		}
		if skill.Strategy == skillsLoader.StrategyAlways {
			alwaysSkills = append(alwaysSkills, skill)
		} else {
			onDemandSkills = append(onDemandSkills, skill)
		}
	}

	// Sort by priority (lower = higher priority)
	sort.Slice(alwaysSkills, func(i, j int) bool {
		return alwaysSkills[i].Priority < alwaysSkills[j].Priority
	})
	sort.Slice(onDemandSkills, func(i, j int) bool {
		return strings.ToLower(onDemandSkills[i].Name) < strings.ToLower(onDemandSkills[j].Name)
	})

	// Build prompt sections
	var builder strings.Builder
	totalEstimatedTokens := 0

	builder.WriteString(fmt.Sprintf("Instruction block %d (external markdown skills):\n", blockNumber))
	builder.WriteString(fmt.Sprintf("Connected skills folder: %s\n\n", resolvedFolder))

	// 1. Always-loaded skills (full content)
	if len(alwaysSkills) > 0 {
		builder.WriteString("Loaded skills (always available):\n\n")
		for _, skill := range alwaysSkills {
			content := skill.Body
			if len(content) > maxDynamicInstructionBytes {
				content = content[:maxDynamicInstructionBytes] + "\n\n[truncated]"
			}

			section := fmt.Sprintf("Instructions from: %s\n%s\n\n", skill.RelativePath, content)
			tokens := estimateTokensApprox(section)
			totalEstimatedTokens += tokens
			builder.WriteString(section)
		}
	}

	// 2. On-demand skills (name + description only)
	if len(onDemandSkills) > 0 {
		builder.WriteString("Available skills (use Read tool to load):\n\n")
		for _, skill := range onDemandSkills {
			var line string
			if skill.Description != "" {
				line = fmt.Sprintf("- %s: %s [%s]\n", skill.Name, skill.Description, skill.RelativePath)
			} else {
				line = fmt.Sprintf("- %s [%s]\n", skill.Name, skill.RelativePath)
			}
			tokens := estimateTokensApprox(line)
			totalEstimatedTokens += tokens
			builder.WriteString(line)
		}
	}

	// 3. Token budget warning
	if config.MaxAutoLoadTokens > 0 && totalEstimatedTokens > config.MaxAutoLoadTokens {
		warningMsg := fmt.Sprintf(
			"\n⚠️  Warning: Auto-loaded skills exceed token budget (%d > %d)\n",
			totalEstimatedTokens, config.MaxAutoLoadTokens,
		)
		builder.WriteString(warningMsg)
		totalEstimatedTokens += estimateTokensApprox(warningMsg)
	}

	return builder.String(), totalEstimatedTokens, ""
}

func syncSettingsToEnv(previous map[string]string, next map[string]string) {
	for key := range previous {
		if _, ok := next[key]; ok {
			continue
		}
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if err := os.Unsetenv(k); err != nil {
			logging.Warn("Failed to unset env var %q removed from settings: %v", k, err)
		}
	}

	// Sync user-provided settings into process env so tools/CLI subprocesses can consume them.
	for key, value := range next {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if err := os.Setenv(k, value); err != nil {
			logging.Warn("Failed to set env var %q from settings: %v", k, err)
		}
	}
}

func sessionProviderAndModel(sess *session.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata["provider"]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata["model"]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
}

func sessionRoutedProviderAndModel(sess *session.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata["routed_provider"]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata["routed_model"]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
}

func setSessionRoutedProviderAndModel(sess *session.Session, requestedProvider config.ProviderType, routedProvider config.ProviderType, routedModel string) bool {
	if sess == nil {
		return false
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}

	changed := false
	if requestedProvider != config.ProviderAutoRouter {
		if _, ok := sess.Metadata["routed_provider"]; ok {
			delete(sess.Metadata, "routed_provider")
			changed = true
		}
		if _, ok := sess.Metadata["routed_model"]; ok {
			delete(sess.Metadata, "routed_model")
			changed = true
		}
		return changed
	}

	nextProvider := strings.TrimSpace(string(routedProvider))
	nextModel := strings.TrimSpace(routedModel)

	currentProvider, _ := sess.Metadata["routed_provider"].(string)
	currentModel, _ := sess.Metadata["routed_model"].(string)

	if strings.TrimSpace(currentProvider) != nextProvider {
		if nextProvider == "" {
			delete(sess.Metadata, "routed_provider")
		} else {
			sess.Metadata["routed_provider"] = nextProvider
		}
		changed = true
	}

	if strings.TrimSpace(currentModel) != nextModel {
		if nextModel == "" {
			delete(sess.Metadata, "routed_model")
		} else {
			sess.Metadata["routed_model"] = nextModel
		}
		changed = true
	}

	return changed
}

func storageSessionProviderAndModel(sess *storage.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata["provider"]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata["model"]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
}

func storageSessionRoutedProviderAndModel(sess *storage.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata["routed_provider"]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata["routed_model"]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
}

func sessionTotalTokens(sess *session.Session) int {
	if sess == nil || sess.Metadata == nil {
		return 0
	}
	input := metadataNumber(sess.Metadata, "total_input_tokens")
	output := metadataNumber(sess.Metadata, "total_output_tokens")
	total := int(input + output)
	if total < 0 {
		return 0
	}
	return total
}

func sessionInputOutputTokens(sess *session.Session) (int, int) {
	if sess == nil || sess.Metadata == nil {
		return 0, 0
	}
	input := int(metadataNumber(sess.Metadata, "total_input_tokens"))
	output := int(metadataNumber(sess.Metadata, "total_output_tokens"))
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	return input, output
}

func storageSessionTotalTokens(sess *storage.Session) int {
	if sess == nil || sess.Metadata == nil {
		return 0
	}
	input := metadataNumber(sess.Metadata, "total_input_tokens")
	output := metadataNumber(sess.Metadata, "total_output_tokens")
	total := int(input + output)
	if total < 0 {
		return 0
	}
	return total
}

func sessionRunDurationSeconds(createdAt time.Time, updatedAt time.Time, status string) int64 {
	end := updatedAt
	if strings.EqualFold(strings.TrimSpace(status), string(session.StatusRunning)) {
		end = time.Now()
	}
	if end.Before(createdAt) {
		return 0
	}
	return int64(end.Sub(createdAt).Seconds())
}

func metadataNumber(metadata map[string]interface{}, key string) float64 {
	if metadata == nil {
		return 0
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return 0
	}

	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case uint:
		return float64(v)
	case uint64:
		return float64(v)
	case uint32:
		return float64(v)
	default:
		return 0
	}
}

func projectToResponse(project *storage.Project) ProjectResponse {
	if project == nil {
		return ProjectResponse{}
	}

	return ProjectResponse{
		ID:        project.ID,
		Name:      project.Name,
		Folder:    project.Folder,
		IsSystem:  project.IsSystem,
		CreatedAt: project.CreatedAt,
		UpdatedAt: project.UpdatedAt,
	}
}

func normalizeFolder(folder *string) *string {
	if folder == nil {
		return nil
	}
	normalized := strings.TrimSpace(*folder)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func normalizeFolders(folders []string) []string {
	if len(folders) == 0 {
		return []string{}
	}

	normalized := make([]string, 0, len(folders))
	seen := make(map[string]struct{}, len(folders))
	for _, raw := range folders {
		folder := strings.TrimSpace(raw)
		if folder == "" {
			continue
		}
		if _, exists := seen[folder]; exists {
			continue
		}
		seen[folder] = struct{}{}
		normalized = append(normalized, folder)
	}

	return normalized
}

func normalizeJobLLMProvider(raw string) string {
	return config.NormalizeProviderRef(raw)
}

func (s *Server) resolveJobProviderType(job *storage.RecurringJob) config.ProviderType {
	if job != nil {
		provider := normalizeJobLLMProvider(job.LLMProvider)
		if provider != "" {
			return config.ProviderType(provider)
		}
	}
	return config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
}

func (s *Server) resolveModelForProvider(providerType config.ProviderType) string {
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback || providerType == config.ProviderAutoRouter {
		return ""
	}
	provider := s.config.Providers[string(providerType)]
	if strings.TrimSpace(provider.Model) != "" {
		return strings.TrimSpace(provider.Model)
	}

	if def := config.GetProviderDefinition(providerType); def != nil && strings.TrimSpace(def.DefaultModel) != "" {
		return strings.TrimSpace(def.DefaultModel)
	}

	return strings.TrimSpace(s.config.DefaultModel)
}

func (s *Server) resolveSessionProviderType(sess *session.Session) config.ProviderType {
	if sess != nil && sess.Metadata != nil {
		if raw, ok := sess.Metadata["provider"]; ok {
			if provider, ok := raw.(string); ok && strings.TrimSpace(provider) != "" {
				return config.ProviderType(config.NormalizeProviderRef(provider))
			}
		}
	}
	return config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
}

func (s *Server) resolveSessionModel(sess *session.Session, providerType config.ProviderType) string {
	if sess != nil && sess.Metadata != nil {
		if raw, ok := sess.Metadata["model"]; ok {
			if model, ok := raw.(string); ok && strings.TrimSpace(model) != "" {
				return strings.TrimSpace(model)
			}
		}
	}
	return s.resolveModelForProvider(providerType)
}

func (s *Server) resolveContextWindowForProvider(providerType config.ProviderType) int {
	if providerType == config.ProviderAutoRouter {
		return 0
	}
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback {
		chain, err := s.fallbackNodesForProvider(providerType)
		if err != nil {
			return 0
		}
		minContext := 0
		for _, node := range chain {
			def := config.GetProviderDefinition(config.ProviderType(node.Provider))
			if def == nil || def.ContextWindow <= 0 {
				continue
			}
			if minContext == 0 || def.ContextWindow < minContext {
				minContext = def.ContextWindow
			}
		}
		return minContext
	}
	if def := config.GetProviderDefinition(providerType); def != nil && def.ContextWindow > 0 {
		return def.ContextWindow
	}
	return 0
}

func (s *Server) createLLMClient(providerType config.ProviderType, model string) (llm.Client, error) {
	if providerType == config.ProviderAutoRouter {
		return nil, fmt.Errorf("automatic router requires dynamic prompt routing")
	}
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback {
		return s.createFallbackChainClient(providerType)
	}
	client, err := s.createBaseLLMClient(providerType, model)
	if err != nil {
		return nil, err
	}
	retries := s.config.LLMRetries
	if retries <= 0 {
		retries = retry.DefaultMaxRetries
	}
	return retry.Wrap(client, retry.WithMaxRetries(retries)), nil
}

func (s *Server) createBaseLLMClient(providerType config.ProviderType, model string) (llm.Client, error) {
	def := config.GetProviderDefinition(providerType)
	if def == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerType)
	}
	if providerType == config.ProviderFallback {
		return nil, fmt.Errorf("fallback aggregate is not a direct provider")
	}

	provider := s.config.Providers[string(providerType)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(def.DefaultURL)
	}
	modelName := strings.TrimSpace(model)
	if modelName == "" {
		modelName = s.resolveModelForProvider(providerType)
	}

	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(providerType)
	}

	// Special handling for Anthropic: OAuth or API key
	if providerType == config.ProviderAnthropic {
		if provider.OAuth != nil && provider.OAuth.AccessToken != "" {
			// Use OAuth
			tokens := &anthropic.OAuthTokens{
				AccessToken:  provider.OAuth.AccessToken,
				RefreshToken: provider.OAuth.RefreshToken,
				ExpiresIn:    int(provider.OAuth.ExpiresAt - time.Now().Unix()),
			}
			return anthropic.NewOAuthClient(tokens, modelName, s.refreshAnthropicOAuthToken), nil
		}
		// Fall through to API key check below
	}

	if def.RequiresKey && apiKey == "" {
		return nil, fmt.Errorf("%s requires an API key (configure provider API key or set %s)", def.DisplayName, s.apiKeyEnvName(providerType))
	}

	switch providerType {
	case config.ProviderGoogle:
		// Google Gemini uses a dedicated client with OpenAI-compatible API + Gemini extensions
		baseURL = normalizeOpenAIBaseURL(baseURL)
		return gemini.NewClient(apiKey, modelName, baseURL), nil
	case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderOpenAI:
		// Other OpenAI-compatible providers
		baseURL = normalizeOpenAIBaseURL(baseURL)
		return lmstudio.NewClient(apiKey, modelName, baseURL), nil
	case config.ProviderAnthropic:
		// Use API key (OAuth case handled above)
		return anthropic.NewClientWithBaseURL(apiKey, modelName, baseURL), nil
	default:
		return anthropic.NewClientWithBaseURL(apiKey, modelName, baseURL), nil
	}
}

func (s *Server) createFallbackChainClient(providerRef config.ProviderType) (llm.Client, error) {
	chain, err := s.fallbackNodesForProvider(providerRef)
	if err != nil {
		return nil, err
	}

	nodes := make([]fallback.Node, 0, len(chain))
	for _, node := range chain {
		ptype := config.ProviderType(node.Provider)
		model := strings.TrimSpace(node.Model)
		client, err := s.createBaseLLMClient(ptype, model)
		if err != nil {
			return nil, fmt.Errorf("fallback node %s/%s is not available: %w", node.Provider, model, err)
		}
		nodes = append(nodes, fallback.Node{
			Name:   node.Provider,
			Model:  model,
			Client: client,
		})
	}
	retries := s.config.LLMRetries
	if retries <= 0 {
		retries = fallback.DefaultMaxRetries
	}
	return fallback.NewClient(nodes, fallback.WithMaxRetries(retries)), nil
}

func normalizeOpenAIBaseURL(raw string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	switch {
	case strings.HasSuffix(baseURL, "/models"):
		baseURL = strings.TrimSuffix(baseURL, "/models")
	case strings.HasSuffix(baseURL, "/chat/completions"):
		baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	}
	return strings.TrimSpace(baseURL)
}

func (s *Server) apiKeyFromEnv(providerType config.ProviderType) string {
	envKey := s.apiKeyEnvName(providerType)
	if envKey != "" {
		if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
			return value
		}
	}
	if providerType == config.ProviderGoogle {
		return strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	return ""
}

func (s *Server) apiKeyEnvName(providerType config.ProviderType) string {
	switch providerType {
	case config.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case config.ProviderKimi:
		return "KIMI_API_KEY"
	case config.ProviderOpenRouter:
		return "OPENROUTER_API_KEY"
	case config.ProviderGoogle:
		return "GOOGLE_API_KEY"
	case config.ProviderOpenAI:
		return "OPENAI_API_KEY"
	default:
		return ""
	}
}

func normalizeFallbackChainNodes(raw []config.FallbackChainNode) []config.FallbackChainNode {
	chain := make([]config.FallbackChainNode, 0, len(raw))
	for _, item := range raw {
		provider := config.NormalizeProviderRef(item.Provider)
		model := strings.TrimSpace(item.Model)
		if provider == "" || model == "" {
			continue
		}
		chain = append(chain, config.FallbackChainNode{Provider: provider, Model: model})
	}
	return chain
}

func legacyProvidersToFallbackNodes(raw []string, resolveModel func(config.ProviderType) string) []config.FallbackChainNode {
	nodes := make([]config.FallbackChainNode, 0, len(raw))
	for _, provider := range raw {
		normalizedProvider := config.NormalizeProviderRef(provider)
		if normalizedProvider == "" {
			continue
		}
		model := strings.TrimSpace(resolveModel(config.ProviderType(normalizedProvider)))
		if model == "" {
			continue
		}
		nodes = append(nodes, config.FallbackChainNode{Provider: normalizedProvider, Model: model})
	}
	return nodes
}

func (s *Server) normalizeAndValidateFallbackChain(raw []config.FallbackChainNode) ([]config.FallbackChainNode, error) {
	chain := normalizeFallbackChainNodes(raw)
	if len(chain) < 2 {
		return nil, fmt.Errorf("fallback chain must contain at least two model nodes")
	}

	seen := make(map[string]struct{}, len(chain))
	for _, node := range chain {
		key := node.Provider + "::" + node.Model
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("fallback chain nodes must not repeat: %s/%s", node.Provider, node.Model)
		}
		seen[key] = struct{}{}

		ptype := config.ProviderType(node.Provider)
		if ptype == config.ProviderFallback {
			return nil, fmt.Errorf("fallback chain cannot include fallback_chain itself")
		}
		def := config.GetProviderDefinition(ptype)
		if def == nil {
			return nil, fmt.Errorf("unsupported provider in fallback chain: %s", node.Provider)
		}
		if !s.providerConfiguredForUse(ptype) {
			return nil, fmt.Errorf("provider %s is not configured or missing required credentials", node.Provider)
		}
	}
	return chain, nil
}

func (s *Server) fallbackChainIsConfigured(chain []config.FallbackChainNode) bool {
	if len(chain) < 2 {
		return false
	}
	validated, err := s.normalizeAndValidateFallbackChain(chain)
	return err == nil && len(validated) >= 2
}

func (s *Server) fallbackNodesForProvider(providerRef config.ProviderType) ([]config.FallbackChainNode, error) {
	ref := config.NormalizeProviderRef(string(providerRef))
	if ref == string(config.ProviderFallback) {
		provider := s.config.Providers[string(config.ProviderFallback)]
		if len(provider.FallbackChainNodes) > 0 {
			return s.normalizeAndValidateFallbackChain(provider.FallbackChainNodes)
		}
		return s.normalizeAndValidateFallbackChain(legacyProvidersToFallbackNodes(provider.FallbackChain, s.resolveModelForProvider))
	}
	if config.IsFallbackAggregateRef(ref) {
		aggregate, _ := s.findFallbackAggregateByRef(ref)
		if aggregate == nil {
			return nil, fmt.Errorf("fallback aggregate not found: %s", ref)
		}
		return s.normalizeAndValidateFallbackChain(aggregate.Chain)
	}
	return nil, fmt.Errorf("provider is not fallback aggregate: %s", ref)
}

func (s *Server) findFallbackAggregateByID(id string) *config.FallbackAggregate {
	normalizedID := config.NormalizeToken(id)
	for i := range s.config.FallbackAggregates {
		if config.NormalizeToken(s.config.FallbackAggregates[i].ID) == normalizedID {
			return &s.config.FallbackAggregates[i]
		}
	}
	return nil
}

func (s *Server) findFallbackAggregateByRef(ref string) (*config.FallbackAggregate, int) {
	id := config.FallbackAggregateIDFromRef(ref)
	if id == "" {
		return nil, -1
	}
	for i := range s.config.FallbackAggregates {
		if config.NormalizeToken(s.config.FallbackAggregates[i].ID) == id {
			return &s.config.FallbackAggregates[i], i
		}
	}
	return nil, -1
}

func (s *Server) providerRefExists(ref string) bool {
	normalized := config.NormalizeProviderRef(ref)
	if config.GetProviderDefinition(config.ProviderType(normalized)) != nil {
		return true
	}
	if config.IsFallbackAggregateRef(normalized) {
		aggregate, _ := s.findFallbackAggregateByRef(normalized)
		return aggregate != nil
	}
	return false
}

func (s *Server) validateProviderRefForExecution(ref string) error {
	normalized := config.NormalizeProviderRef(ref)
	if normalized == "" {
		return fmt.Errorf("provider is empty")
	}
	ptype := config.ProviderType(normalized)
	if def := config.GetProviderDefinition(ptype); def != nil {
		if ptype == config.ProviderFallback {
			_, err := s.fallbackNodesForProvider(ptype)
			return err
		}
		if ptype == config.ProviderAutoRouter {
			provider := s.config.Providers[string(config.ProviderAutoRouter)]
			return s.validateAutoRouterProvider(provider)
		}
		if !s.providerConfiguredForUse(ptype) {
			return fmt.Errorf("provider is not configured")
		}
		return nil
	}
	if config.IsFallbackAggregateRef(normalized) {
		_, err := s.fallbackNodesForProvider(ptype)
		return err
	}
	return fmt.Errorf("provider not found")
}

func (s *Server) providerConfiguredForUse(providerType config.ProviderType) bool {
	def := config.GetProviderDefinition(providerType)
	if def == nil || providerType == config.ProviderFallback || providerType == config.ProviderAutoRouter {
		return false
	}
	provider := s.config.Providers[string(providerType)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(def.DefaultURL)
	}
	if baseURL == "" {
		return false
	}
	if !def.RequiresKey {
		return true
	}
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(providerType)
	}
	return apiKey != ""
}

// handleBrowserChromeProfileStatus returns the status of the browser_chrome agent profile
func (s *Server) handleBrowserChromeProfileStatus(w http.ResponseWriter, r *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, `{"error": "failed to get home directory"}`, http.StatusInternalServerError)
		return
	}

	// Use the same path as browser_chrome.go tool and handleBrowserChromeLaunch
	chromeAgentPath := filepath.Join(home, "Library", "Application Support", "Google", "ChromeAgent")

	info, err := os.Stat(chromeAgentPath)
	exists := err == nil && info.IsDir()

	var lastUsed string
	if exists {
		lastUsed = info.ModTime().Format(time.RFC3339)
	}

	response := map[string]interface{}{
		"exists": exists,
		"path":   chromeAgentPath,
	}
	if exists {
		response["lastUsed"] = lastUsed
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, `{"error": "failed to encode response"}`, http.StatusInternalServerError)
	}
}

// handleBrowserChromeCreateProfile creates the agent profile by copying essential files from main profile
func (s *Server) handleBrowserChromeCreateProfile(w http.ResponseWriter, r *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, `{"error": "failed to get home directory"}`, http.StatusInternalServerError)
		return
	}

	mainProfile := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	agentProfile := filepath.Join(mainProfile, "AgentProfile")

	// Check if agent profile already exists
	if info, err := os.Stat(agentProfile); err == nil && info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Agent profile already exists. Delete it first if you want to recreate.",
			"path":    agentProfile,
		})
		return
	}

	// Create agent profile directory structure
	agentDefaultDir := filepath.Join(agentProfile, "Default")
	if err := os.MkdirAll(agentDefaultDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "failed to create agent profile: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	mainDefaultDir := filepath.Join(mainProfile, "Default")

	// Copy essential files
	filesToCopy := []string{
		"Cookies",
		"Login Data",
		"Preferences",
		"Network Persistent State",
		"Secure Preferences",
		"Web Data",
		"History",
		"Favicons",
		"Bookmarks",
	}

	copied := 0
	var failedFiles []string
	for _, file := range filesToCopy {
		src := filepath.Join(mainDefaultDir, file)
		dst := filepath.Join(agentDefaultDir, file)

		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}

		// Use cp -c for APFS clone (fast, copy-on-write)
		cmd := exec.Command("cp", "-c", src, dst)
		if err := cmd.Run(); err != nil {
			failedFiles = append(failedFiles, file)
			continue
		}
		copied++
	}

	// Also copy Local State from profile root
	localStateSrc := filepath.Join(mainProfile, "Local State")
	localStateDst := filepath.Join(agentProfile, "Local State")
	if _, err := os.Stat(localStateSrc); err == nil {
		cmd := exec.Command("cp", "-c", localStateSrc, localStateDst)
		if err := cmd.Run(); err != nil {
			failedFiles = append(failedFiles, "Local State")
		} else {
			copied++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"message":     fmt.Sprintf("Agent profile created with %d files", copied),
		"path":        agentProfile,
		"filesCopied": copied,
		"failedFiles": failedFiles,
	})
}

// handleBrowserChromeLaunch launches Chrome with a dedicated user-data-dir for the agent.
// This directory is SEPARATE from the default Chrome directory, which:
// 1. Allows remote debugging (Chrome blocks it on default directory)
// 2. Preserves encrypted credentials (logins persist between sessions)
// 3. Both UI button and agent tool use the SAME directory
func (s *Server) handleBrowserChromeLaunch(w http.ResponseWriter, r *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, `{"error": "failed to get home directory"}`, http.StatusInternalServerError)
		return
	}

	// Use a SEPARATE Chrome user-data directory (not inside default Chrome)
	// This is the same path used by browser_chrome.go tool
	chromeAgentDir := filepath.Join(home, "Library", "Application Support", "Google", "ChromeAgent")

	logging.Info("Using ChromeAgent directory: %s", chromeAgentDir)

	// Create directory if it doesn't exist
	profileExists := false
	if _, err := os.Stat(chromeAgentDir); err == nil {
		profileExists = true
		logging.Info("ChromeAgent directory already exists")
	} else {
		logging.Info("Creating ChromeAgent directory: %s", chromeAgentDir)
		if err := os.MkdirAll(chromeAgentDir, 0755); err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to create ChromeAgent directory: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	// Launch Chrome with dedicated user-data-dir and remote debugging
	chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

	args := []string{
		"--user-data-dir=" + chromeAgentDir,
		"--remote-debugging-port=9223",
		"--no-first-run",
		"--no-default-browser-check",
	}

	logging.Info("Launching Chrome with user-data-dir: %s", chromeAgentDir)

	cmd := exec.Command(chromePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "failed to launch Chrome: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	logging.Info("Chrome launched with ChromeAgent profile, PID: %d", cmd.Process.Pid)

	message := "Chrome opened with agent profile. Log in to websites here - the agent will use these sessions."
	if !profileExists {
		message = "Chrome opened with NEW agent profile. Please log in to websites you want the agent to access."
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"message":       message,
		"pid":           cmd.Process.Pid,
		"profile":       chromeAgentDir,
		"profileExists": profileExists,
	})
}

// isChromeRunning checks if Chrome is currently running
func isChromeRunning() bool {
	cmd := exec.Command("pgrep", "-x", "Google Chrome")
	err := cmd.Run()
	return err == nil
}
