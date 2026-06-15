// routes.go keeps route wiring separate from handler implementations while preserving
// the original registration order, which is important for chi route matching behavior.
package http

import (
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// setupRoutes configures all API routes.
func (s *Server) setupRoutes() {
	r := chi.NewRouter()

	// Middleware. The access logger is disabled by default so TUI mode remains clean;
	// HTTP-only/server mode enables it explicitly before Run.
	r.Use(s.httpAccessLogMiddleware)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(5 * time.Minute))

	// CORS configuration - allow all origins for flexibility
	allowedOrigins := s.config.EffectiveCORSAllowedOrigins()
	allowCredentials := true
	for _, origin := range allowedOrigins {
		if strings.TrimSpace(origin) == "*" {
			allowCredentials = false // Must be false when wildcard origin is allowed.
			break
		}
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: allowCredentials,
		MaxAge:           300,
	}))

	// Health check
	r.Get("/health", s.handleHealth)

	// A2A Agent Card (Well-Known URI per A2A spec)
	r.Get("/.well-known/agent-card.json", s.handleAgentCard)

	s.registerA2ARoutes(r)
	s.registerSettingsRoutes(r)
	s.registerLLMProxyRoutes(r)
	s.registerProviderRoutes(r)
	s.registerIntegrationRoutes(r)
	s.registerMCPRoutes(r)
	s.registerSpeechRoutes(r)
	s.registerMeetingRoutes(r)
	s.registerAssetRoutes(r)
	s.registerDeviceRoutes(r)
	s.registerBrowserChromeRoutes(r)
	s.registerBrowserExtensionRoutes(r)
	s.registerSessionRoutes(r)
	s.registerSessionTemplateRoutes(r)
	s.registerProjectRoutes(r)
	s.registerJobRoutes(r)
	s.registerMindRoutes(r)
	s.registerSubAgentRoutes(r)
	s.registerToolRoutes(r)
	s.registerSkillRoutes(r)

	s.router = r
}

func (s *Server) registerA2ARoutes(r chi.Router) {
	// A2A outbound chat (local session -> remote agent via tunnel).
	r.Route("/a2a", func(r chi.Router) {
		r.Post("/messages/send", s.handleA2AMessageSend)
		r.Post("/messages/send/stream", s.handleA2AMessageSendStream)
		r.Post("/outbound/sessions", s.handleCreateA2AOutboundSession)
		r.Post("/outbound/sessions/{sessionID}/chat", s.handleA2AOutboundChat)
		r.Post("/outbound/sessions/{sessionID}/chat/stream", s.handleA2AOutboundChatStream)
	})
}

func (s *Server) registerSettingsRoutes(r chi.Router) {
	// App settings (tokens/secrets/runtime options)
	r.Get("/settings", s.handleGetSettings)
	r.Put("/settings", s.handleUpdateSettings)
	r.Post("/settings/instruction-estimate", s.handleEstimateInstructionPrompt)
}

func (s *Server) registerLLMProxyRoutes(r chi.Router) {
	// OpenAI-compatible proxy to this agent's configured providers.
	r.Route("/v1", func(r chi.Router) {
		r.Get("/models", s.handleLLMProxyModels)
		r.Post("/chat/completions", s.handleLLMProxyChatCompletions)
		r.Get("/providers/{providerRef}/models", s.handleLLMProxyProviderModels)
		r.Post("/providers/{providerRef}/chat/completions", s.handleLLMProxyProviderChatCompletions)
	})
}

func (s *Server) registerProviderRoutes(r chi.Router) {
	// LLM provider configuration
	r.Route("/providers", func(r chi.Router) {
		r.Get("/", s.handleListProviders)
		r.Put("/active", s.handleSetActiveProvider)
		r.Post("/fallback-aggregates", s.handleCreateFallbackAggregate)
		r.Get("/lmstudio/models", s.handleListLMStudioModels)
		r.Get("/kimi/models", s.handleListKimiModels)
		r.Get("/google/models", s.handleListGoogleModels)
		r.Get("/openai/models", s.handleListOpenAIModels)
		r.Get("/openai_codex/models", s.handleListOpenAICodexModels)
		r.Get("/openrouter/models", s.handleListOpenRouterModels)
		r.Get("/opencode_zen/models", s.handleListOpenCodeZenModels)
		r.Get("/anthropic/models", s.handleListAnthropicModels)
		r.Put("/{providerType}", s.handleUpdateProvider)
		r.Delete("/{providerType}", s.handleDeleteProvider)
		r.Post("/{providerType}/test", s.handleTestProvider)
		r.Post("/test-all", s.handleTestAllProviders)

		// OpenAI Codex OAuth (import from Codex auth cache)
		r.Post("/openai_codex/oauth/import", s.handleOpenAICodexOAuthImport)
		r.Get("/openai_codex/oauth/status", s.handleOpenAICodexOAuthStatus)
		r.Delete("/openai_codex/oauth", s.handleOpenAICodexOAuthDisconnect)
	})
}

func (s *Server) registerIntegrationRoutes(r chi.Router) {
	// External channel integrations
	r.Route("/integrations", func(r chi.Router) {
		r.Get("/", s.handleListIntegrations)
		r.Post("/", s.handleCreateIntegration)
		r.Post("/leonardo/models", s.handleListLeonardoModels)
		r.Post("/telegram/chat-ids", s.handleDiscoverTelegramChats)
		// A2A tunnel status endpoints (no integrationID in path)
		r.Get("/a2_registry/tunnel-status", s.handleA2ATunnelStatus)
		r.Get("/a2_registry/tunnel-status/stream", s.handleA2ATunnelStatusStream)
		r.Post("/a2_registry/tunnel-reconnect", s.handleA2ATunnelReconnect)
		r.Post("/a2_registry/register-current", s.handleRegisterCurrentA2AAgent)
		r.Get("/a2_registry/local-agents", s.handleListLocalDockerAgents)
		r.Post("/a2_registry/local-agents", s.handleCreateLocalDockerAgent)
		r.Post("/a2_registry/local-agents/from-yaml", s.handleCreateLocalDockerAgentsFromYAML)
		r.Post("/a2_registry/local-agents/build-image", s.handleBuildLocalDockerAgentImage)
		r.Post("/a2_registry/local-agents/{containerID}/start", s.handleStartLocalDockerAgent)
		r.Post("/a2_registry/local-agents/{containerID}/stop", s.handleStopLocalDockerAgent)
		r.Delete("/a2_registry/local-agents/{containerID}", s.handleRemoveLocalDockerAgent)
		r.Get("/a2_registry/local-agents/{containerID}/logs", s.handleLocalDockerAgentLogs)
		r.Post("/a2_registry/local-agents/{containerID}/register", s.handleRegisterLocalDockerAgent)
		r.Get("/{integrationID}", s.handleGetIntegration)
		r.Put("/{integrationID}", s.handleUpdateIntegration)
		r.Delete("/{integrationID}", s.handleDeleteIntegration)
		r.Post("/{integrationID}/test", s.handleTestIntegration)
	})
}

func (s *Server) registerMCPRoutes(r chi.Router) {
	// MCP server registry and diagnostics
	r.Route("/mcp/servers", func(r chi.Router) {
		r.Get("/", s.handleListMCPServers)
		r.Post("/", s.handleCreateMCPServer)
		r.Get("/{serverID}", s.handleGetMCPServer)
		r.Put("/{serverID}", s.handleUpdateMCPServer)
		r.Delete("/{serverID}", s.handleDeleteMCPServer)
		r.Post("/{serverID}/test", s.handleTestMCPServer)
	})
}

func (s *Server) registerSpeechRoutes(r chi.Router) {
	// Speech/TTS helpers (proxied through backend)
	r.Route("/speech", func(r chi.Router) {
		r.Get("/voices", s.handleListSpeechVoices)
		r.Get("/piper/voices", s.handleListPiperVoices)
		r.Post("/completion", s.handleCompletionSpeech)
		r.Post("/transcribe", s.handleTranscribeSpeech)
		r.Get("/clips/{clipID}", s.handleGetSpeechClip)
	})
}

func (s *Server) registerMeetingRoutes(r chi.Router) {
	// Meeting capture persistence (audio + markdown notes).
	r.Route("/meetings", func(r chi.Router) {
		r.Post("/save", s.handleSaveMeetingArtifacts)
		r.Get("/list", s.handleListMeetingArtifacts)
		r.Get("/audio", s.handleGetMeetingAudio)
		r.Post("/delete", s.handleDeleteMeetingArtifacts)
	})
}

func (s *Server) registerAssetRoutes(r chi.Router) {
	// Local assets exposed for session UI rendering.
	r.Route("/assets", func(r chi.Router) {
		r.Get("/images", s.handleGetImageAsset)
	})
}

func (s *Server) registerDeviceRoutes(r chi.Router) {
	// Local device helpers.
	r.Route("/devices", func(r chi.Router) {
		r.Get("/cameras", s.handleListCameraDevices)
	})
}

func (s *Server) registerBrowserChromeRoutes(r chi.Router) {
	// Browser Chrome tool management
	r.Route("/browser-chrome", func(r chi.Router) {
		r.Get("/profile-status", s.handleBrowserChromeProfileStatus)
		r.Post("/create-profile", s.handleBrowserChromeCreateProfile)
		r.Post("/launch", s.handleBrowserChromeLaunch)
	})
}

func (s *Server) registerSessionRoutes(r chi.Router) {
	// Session endpoints
	r.Route("/sessions", func(r chi.Router) {
		r.Get("/", s.handleListSessions)
		r.Post("/", s.handleCreateSession)
		r.Get("/{sessionID}", s.handleGetSession)
		r.Delete("/{sessionID}", s.handleDeleteSession)
		r.Post("/{sessionID}/cancel", s.handleCancelSession)
		r.Put("/{sessionID}/project", s.handleUpdateSessionProject)
		r.Put("/{sessionID}/provider", s.handleUpdateSessionProvider)
		r.Post("/{sessionID}/chat", s.handleChat)
		r.Post("/{sessionID}/chat/stream", s.handleChatStream)
		r.Post("/{sessionID}/inject", s.handleInjectSessionMessage)
		r.Get("/{sessionID}/question", s.handleGetPendingQuestion)
		r.Post("/{sessionID}/answer", s.handleAnswerQuestion)
		r.Post("/{sessionID}/start", s.handleStartSession)
		r.Get("/{sessionID}/task-progress", s.handleGetTaskProgress)
	})
}

func (s *Server) registerSessionTemplateRoutes(r chi.Router) {
	// Reusable templates for pre-filling new session prompts.
	r.Route("/session-templates", func(r chi.Router) {
		r.Get("/", s.handleListSessionTemplates)
		r.Post("/", s.handleCreateSessionTemplate)
		r.Get("/{templateID}", s.handleGetSessionTemplate)
		r.Put("/{templateID}", s.handleUpdateSessionTemplate)
		r.Delete("/{templateID}", s.handleDeleteSessionTemplate)
	})
}

func (s *Server) registerProjectRoutes(r chi.Router) {
	// Projects endpoints (optional grouping for sessions)
	r.Route("/projects", func(r chi.Router) {
		r.Get("/", s.handleListProjects)
		r.Post("/", s.handleCreateProject)
		// Static routes must come before dynamic {projectID} route.
		r.Get("/tree", s.handleListProjectTree)
		r.Get("/git/status", s.handleProjectGitStatus)
		r.Post("/git/init", s.handleProjectGitInit)
		r.Get("/git/diff", s.handleProjectGitFileDiff)
		r.Get("/git/branch-changes", s.handleProjectGitBranchChanges)
		r.Get("/git/branch-diff", s.handleProjectGitBranchDiff)
		r.Get("/git/history", s.handleProjectGitHistory)
		r.Get("/git/commit-files", s.handleProjectGitCommitFiles)
		r.Get("/git/commit-diff", s.handleProjectGitCommitDiff)
		r.Get("/git/pr-description", s.handleGetProjectGitPRDescription)
		r.Put("/git/pr-description", s.handleSaveProjectGitPRDescription)
		r.Get("/tests", s.handleProjectTestsDiscovery)
		r.Get("/tests/branch-cache", s.handleProjectTestsBranchCache)
		r.Post("/tests/branch-cache/refresh", s.handleProjectTestsBranchCacheRefresh)
		r.Post("/tests/run", s.handleProjectTestsRun)
		r.Post("/tests/coverage", s.handleProjectTestsCoverage)
		r.Post("/git/stage", s.handleProjectGitStageFile)
		r.Post("/git/stage-all", s.handleProjectGitStageAllFiles)
		r.Post("/git/unstage", s.handleProjectGitUnstageFile)
		r.Post("/git/discard", s.handleProjectGitDiscardFile)
		r.Post("/git/commit-message", s.handleProjectGitCommitMessageSuggestion)
		r.Post("/git/pr-description/generate", s.handleGenerateProjectGitPRDescription)
		r.Post("/git/push", s.handleProjectGitPush)
		r.Post("/git/pull", s.handleProjectGitPull)
		r.Post("/git/checkout", s.handleProjectGitCheckout)
		r.Post("/git/commit", s.handleProjectGitCommit)
		r.Get("/file", s.handleGetProjectFile)
		r.Get("/file/raw", s.handleGetProjectFileRaw)
		r.Get("/search", s.handleProjectSearch)
		r.Post("/file", s.handleUpsertProjectFile)
		r.Put("/file", s.handleUpsertProjectFile)
		r.Delete("/file", s.handleDeleteProjectFile)
		r.Post("/file/move", s.handleMoveProjectFile)
		r.Post("/folder", s.handleCreateProjectFolder)
		r.Post("/rename", s.handleRenameProjectEntry)
		// Dynamic route must come last.
		r.Get("/{projectID}", s.handleGetProject)
		r.Put("/{projectID}", s.handleUpdateProject)
		r.Delete("/{projectID}", s.handleDeleteProject)

		r.Get("/{projectID}/databases", s.handleListProjectDatabases)
		r.Post("/{projectID}/databases", s.handleCreateProjectDatabase)
		r.Put("/{projectID}/databases/{dbID}", s.handleUpdateProjectDatabase)
		r.Delete("/{projectID}/databases/{dbID}", s.handleDeleteProjectDatabase)
		r.Get("/{projectID}/databases/{dbID}/tables", s.handleProjectDatabaseListTables)
		r.Post("/{projectID}/databases/{dbID}/query", s.handleProjectDatabaseQuery)
	})
}

func (s *Server) registerJobRoutes(r chi.Router) {
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
}

func (s *Server) registerMindRoutes(r chi.Router) {
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
}

func (s *Server) registerSubAgentRoutes(r chi.Router) {
	// Sub-agents
	r.Route("/sub-agents", func(r chi.Router) {
		r.Get("/", s.handleListSubAgents)
		r.Post("/", s.handleCreateSubAgent)
		r.Post("/instruction-estimate", s.handleEstimateSubAgentInstructionsDraft)
		r.Get("/{subAgentID}", s.handleGetSubAgent)
		r.Put("/{subAgentID}", s.handleUpdateSubAgent)
		r.Delete("/{subAgentID}", s.handleDeleteSubAgent)
		r.Post("/{subAgentID}/instruction-estimate", s.handleEstimateSubAgentInstructions)
		r.Get("/{subAgentID}/yaml", s.handleExportSubAgentYAML)
	})

	// Unified agent model across host sub-agents and local Docker agents.
	r.Route("/unified-agents", func(r chi.Router) {
		r.Get("/", s.handleListUnifiedAgents)
		r.Post("/import-yaml", s.handleImportAgentYAML)
		r.Post("/{agentDefID}/start", s.handleStartUnifiedAgent)
		r.Get("/{agentDefID}/yaml", s.handleExportAgentDefinitionYAML)
		r.Delete("/{agentDefID}", s.handleDeleteAgentDefinition)
	})
}

func (s *Server) registerToolRoutes(r chi.Router) {
	// Tool definitions (for UI tool selection in sub-agent config)
	r.Get("/tools/definitions", s.handleListToolDefinitions)
}

func (s *Server) registerSkillRoutes(r chi.Router) {
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
}
