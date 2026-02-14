package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/gratheon/aagent/internal/agent"
	"github.com/gratheon/aagent/internal/config"
	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/llm/anthropic"
	"github.com/gratheon/aagent/internal/llm/fallback"
	"github.com/gratheon/aagent/internal/llm/lmstudio"
	"github.com/gratheon/aagent/internal/logging"
	"github.com/gratheon/aagent/internal/session"
	"github.com/gratheon/aagent/internal/storage"
	"github.com/gratheon/aagent/internal/tools"
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
}

const thinkingJobIDSettingKey = "A2GENT_THINKING_JOB_ID"
const thinkingProjectID = "project-thinking"
const thinkingProjectName = "Thinking"

// NewServer creates a new HTTP server instance
func NewServer(
	cfg *config.Config,
	llmClient llm.Client,
	toolManager *tools.Manager,
	sessionManager *session.Manager,
	store storage.Store,
	port int,
) *Server {
	s := &Server{
		config:         cfg,
		llmClient:      llmClient,
		toolManager:    toolManager,
		sessionManager: sessionManager,
		store:          store,
		port:           port,
	}

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

	// LLM provider configuration
	r.Route("/providers", func(r chi.Router) {
		r.Get("/", s.handleListProviders)
		r.Put("/active", s.handleSetActiveProvider)
		r.Post("/fallback-aggregates", s.handleCreateFallbackAggregate)
		r.Get("/lmstudio/models", s.handleListLMStudioModels)
		r.Get("/kimi/models", s.handleListKimiModels)
		r.Get("/google/models", s.handleListGoogleModels)
		r.Get("/openai/models", s.handleListOpenAIModels)
		r.Put("/{providerType}", s.handleUpdateProvider)
		r.Delete("/{providerType}", s.handleDeleteProvider)
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

	// Speech/TTS helpers (proxied through backend)
	r.Route("/speech", func(r chi.Router) {
		r.Get("/voices", s.handleListSpeechVoices)
		r.Post("/completion", s.handleCompletionSpeech)
	})

	// Session endpoints
	r.Route("/sessions", func(r chi.Router) {
		r.Get("/", s.handleListSessions)
		r.Post("/", s.handleCreateSession)
		r.Get("/{sessionID}", s.handleGetSession)
		r.Delete("/{sessionID}", s.handleDeleteSession)
		r.Put("/{sessionID}/project", s.handleUpdateSessionProject)
		r.Post("/{sessionID}/chat", s.handleChat)
		r.Post("/{sessionID}/chat/stream", s.handleChatStream)
	})

	// Projects endpoints (optional grouping for sessions)
	r.Route("/projects", func(r chi.Router) {
		r.Get("/", s.handleListProjects)
		r.Post("/", s.handleCreateProject)
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
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	ParentID  string            `json:"parent_id,omitempty"`
	ProjectID string            `json:"project_id,omitempty"`
	Provider  string            `json:"provider,omitempty"`
	Model     string            `json:"model,omitempty"`
	Title     string            `json:"title"`
	Status    string            `json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Messages  []MessageResponse `json:"messages"`
}

// MessageResponse represents a message in a session
type MessageResponse struct {
	Role        string                 `json:"role"`
	Content     string                 `json:"content"`
	ToolCalls   []ToolCallResponse     `json:"tool_calls,omitempty"`
	ToolResults []ToolResultResponse   `json:"tool_results,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	Timestamp   time.Time              `json:"timestamp"`
}

// ToolCallResponse represents a tool call
type ToolCallResponse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResultResponse represents a tool result
type ToolResultResponse struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
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
	Type     string            `json:"type"`
	Delta    string            `json:"delta,omitempty"`
	Content  string            `json:"content,omitempty"`
	Messages []MessageResponse `json:"messages,omitempty"`
	Status   string            `json:"status,omitempty"`
	Usage    *UsageResponse    `json:"usage,omitempty"`
	Error    string            `json:"error,omitempty"`
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
	Title              string    `json:"title"`
	Status             string    `json:"status"`
	TotalTokens        int       `json:"total_tokens"`
	RunDurationSeconds int64     `json:"run_duration_seconds"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// --- Recurring Jobs Request/Response types ---

// CreateJobRequest represents a request to create a recurring job
type CreateJobRequest struct {
	Name         string `json:"name"`
	ScheduleText string `json:"schedule_text"` // Natural language schedule
	TaskPrompt   string `json:"task_prompt"`
	LLMProvider  string `json:"llm_provider,omitempty"`
	Enabled      bool   `json:"enabled"`
}

// UpdateJobRequest represents a request to update a recurring job
type UpdateJobRequest struct {
	Name         string  `json:"name"`
	ScheduleText string  `json:"schedule_text"`
	TaskPrompt   string  `json:"task_prompt"`
	LLMProvider  *string `json:"llm_provider,omitempty"`
	Enabled      *bool   `json:"enabled,omitempty"`
}

// JobResponse represents a recurring job response
type JobResponse struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	ScheduleHuman string     `json:"schedule_human"`
	ScheduleCron  string     `json:"schedule_cron"`
	TaskPrompt    string     `json:"task_prompt"`
	LLMProvider   string     `json:"llm_provider,omitempty"`
	Enabled       bool       `json:"enabled"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	NextRunAt     *time.Time `json:"next_run_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
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
	Settings map[string]string `json:"settings"`
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
	Folders   []string  `json:"folders"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateProjectRequest struct {
	Name    string   `json:"name"`
	Folders []string `json:"folders"`
}

type UpdateProjectRequest struct {
	Name    *string   `json:"name,omitempty"`
	Folders *[]string `json:"folders,omitempty"`
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
	s.jsonResponse(w, http.StatusOK, SettingsResponse{Settings: settings})
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
	s.jsonResponse(w, http.StatusOK, SettingsResponse{Settings: req.Settings})
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
		if def.RequiresKey {
			configured = configured && hasAPIKey
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
	s.handleListOpenAICompatibleModels(w, r, config.ProviderGoogle, "Google Gemini")
}

func (s *Server) handleListOpenAIModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderOpenAI, "OpenAI")
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
		projectID := ""
		if sess.ProjectID != nil {
			projectID = *sess.ProjectID
		}
		items[i] = SessionListItem{
			ID:                 sess.ID,
			AgentID:            sess.AgentID,
			ProjectID:          projectID,
			Provider:           provider,
			Model:              model,
			Title:              sess.Title,
			Status:             string(sess.Status),
			TotalTokens:        sessionTotalTokens(sess),
			RunDurationSeconds: sessionRunDurationSeconds(sess.CreatedAt, sess.UpdatedAt, string(sess.Status)),
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

	sess, err := s.sessionManager.Create(req.AgentID)
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

	resp := s.sessionToResponse(sess)
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	if err := s.sessionManager.Delete(sessionID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete session: "+err.Error())
		return
	}

	logging.LogSession("deleted", sessionID, "via HTTP")
	w.WriteHeader(http.StatusNoContent)
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

	// Add user message to session
	sess.AddUserMessage(req.Message)

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	target, err := s.resolveExecutionTarget(r.Context(), providerType, model, req.Message)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		s.sessionManager.Save(sess)
		s.errorResponse(w, http.StatusBadRequest, "Provider configuration error: "+err.Error())
		return
	}

	// Create agent config
	agentConfig := agent.Config{
		Name:          sess.AgentID,
		Model:         target.Model,
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}

	// Create agent instance
	ag := agent.New(agentConfig, target.Client, s.toolManager, s.sessionManager)

	// Run the agent (this is synchronous for now)
	ctx := r.Context()
	content, usage, err := ag.Run(ctx, sess, req.Message)
	if err != nil {
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

	// Add user message before streaming begins.
	sess.AddUserMessage(req.Message)

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	target, err := s.resolveExecutionTarget(r.Context(), providerType, model, req.Message)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		s.sessionManager.Save(sess)
		s.errorResponse(w, http.StatusBadRequest, "Provider configuration error: "+err.Error())
		return
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
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}
	ag := agent.New(agentConfig, target.Client, s.toolManager, s.sessionManager)

	ctx := r.Context()
	content, usage, err := ag.RunWithEvents(ctx, sess, req.Message, func(ev agent.Event) {
		if ev.Type == agent.EventAssistantDelta {
			_ = writeEvent(ChatStreamEvent{
				Type:  "assistant_delta",
				Delta: ev.Delta,
			})
		}
	})

	if err != nil {
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
	if req.TaskPrompt == "" {
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
		ID:            uuid.New().String(),
		Name:          req.Name,
		ScheduleHuman: req.ScheduleText,
		ScheduleCron:  cronExpr,
		TaskPrompt:    req.TaskPrompt,
		LLMProvider:   llmProvider,
		Enabled:       req.Enabled,
		CreatedAt:     now,
		UpdatedAt:     now,
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
	if req.TaskPrompt != "" {
		job.TaskPrompt = req.TaskPrompt
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
			Title:              sess.Title,
			Status:             sess.Status,
			TotalTokens:        storageSessionTotalTokens(sess),
			RunDurationSeconds: sessionRunDurationSeconds(sess.CreatedAt, sess.UpdatedAt, sess.Status),
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
		MaxSteps:      1, // Only need one response
		Temperature:   0, // Deterministic output
		ContextWindow: target.ContextWindow,
	}

	ag := agent.New(agentConfig, target.Client, s.toolManager, s.sessionManager)
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
	if thinking, thinkErr := s.isProtectedThinkingJob(job.ID); thinkErr != nil {
		logging.Warn("Failed to check thinking job for project assignment: %v", thinkErr)
	} else if thinking {
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

	target, clientErr := s.resolveExecutionTarget(ctx, providerType, model, job.TaskPrompt)
	if clientErr != nil {
		exec.Status = "failed"
		exec.Error = "Failed to initialize provider: " + clientErr.Error()
		finishedAt := time.Now()
		exec.FinishedAt = &finishedAt
		s.store.SaveJobExecution(exec)
		return exec, nil
	}

	// Run the agent with the job's task prompt
	agentConfig := agent.Config{
		Name:          "job-runner",
		Model:         target.Model,
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}
	ag := agent.New(agentConfig, target.Client, s.toolManager, s.sessionManager)
	sess.AddUserMessage(job.TaskPrompt)
	output, _, err := ag.Run(ctx, sess, job.TaskPrompt)

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
		Folders:   []string{},
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
		ID:            job.ID,
		Name:          job.Name,
		ScheduleHuman: job.ScheduleHuman,
		ScheduleCron:  job.ScheduleCron,
		TaskPrompt:    job.TaskPrompt,
		LLMProvider:   job.LLMProvider,
		Enabled:       job.Enabled,
		LastRunAt:     job.LastRunAt,
		NextRunAt:     job.NextRunAt,
		CreatedAt:     job.CreatedAt,
		UpdatedAt:     job.UpdatedAt,
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
	return SessionResponse{
		ID:        sess.ID,
		AgentID:   sess.AgentID,
		ParentID:  parentID,
		ProjectID: projectID,
		Provider:  provider,
		Model:     model,
		Title:     sess.Title,
		Status:    string(sess.Status),
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		Messages:  s.messagesToResponse(sess.Messages),
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
		Folders:   normalizeFolders(req.Folders),
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
	if req.Folders != nil {
		project.Folders = normalizeFolders(*req.Folders)
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

	folders := project.Folders
	if folders == nil {
		folders = []string{}
	}

	return ProjectResponse{
		ID:        project.ID,
		Name:      project.Name,
		Folders:   folders,
		CreatedAt: project.CreatedAt,
		UpdatedAt: project.UpdatedAt,
	}
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
	return s.createBaseLLMClient(providerType, model)
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
	if def.RequiresKey && apiKey == "" {
		return nil, fmt.Errorf("%s requires an API key (configure provider API key or set %s)", def.DisplayName, s.apiKeyEnvName(providerType))
	}

	switch providerType {
	case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderGoogle, config.ProviderOpenAI:
		baseURL = normalizeOpenAIBaseURL(baseURL)
		return lmstudio.NewClient(apiKey, modelName, baseURL), nil
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
	return fallback.NewClient(nodes), nil
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
