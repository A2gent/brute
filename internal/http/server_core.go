// server_core.go keeps the server lifecycle pieces split out of the former server.go without changing HTTP behavior.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/a2atunnel"
	"github.com/A2gent/brute/internal/approval"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/filesearch"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/runtimeenv"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
	"github.com/go-chi/chi/v5"
)

// Server represents the HTTP API server
type Server struct {
	config                   *config.Config
	llmClient                llm.Client
	openRouterModelsClient   openRouterModelsHTTPClient
	toolManager              *tools.Manager
	sessionManager           *session.Manager
	store                    storage.Store
	router                   chi.Router
	port                     int
	portMu                   sync.RWMutex
	portReady                chan int
	speechClips              *speechcache.Store
	runParentCtx             context.Context
	activeRunsMu             sync.Mutex
	activeRuns               map[string]map[string]context.CancelFunc
	sessionEventsMu          sync.Mutex
	sessionEventSubs         map[string]map[chan ChatStreamEvent]struct{}
	serialQueueMu            sync.Mutex
	serialQueueWorkers       map[string]struct{}
	chromeExtensionBridge    *chromeExtensionBridge
	httpAccessLogMu          sync.RWMutex
	httpAccessLogWriter      io.Writer
	httpAccessLogEnabled     bool
	httpAccessLogSeq         uint64
	dockerRuntime            *dockerRuntimeManager
	contextCompressor        *contextcompress.Compressor
	claudeHealthCache        *claudeHealthCache
	approvalBroker           *approval.Broker
	approvalAuditMu          sync.Mutex
	approvalResolvePayloadMu sync.Mutex
	approvalResolvePayload   map[string]approvalResolvePayload
	mcpBridge                *mcpBridgeState
	writeTaskSource          func(string, []byte, os.FileMode) error
	removeTaskSource         func(string) error

	// A2A gRPC tunnel (managed by a2a_tunnel.go)
	tunnelMu     sync.Mutex
	tunnelClient *a2atunnel.TunnelClient
	tunnelCancel context.CancelFunc
}

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
		diskDir := ""
		if cfg != nil && strings.TrimSpace(cfg.DataPath) != "" {
			diskDir = filepath.Join(cfg.DataPath, "speech-clips")
		}
		if diskDir != "" {
			// Use disk-backed clips when the server owns the store so audio controls in
			// old session messages can recover clips after memory TTL/restart.
			speechClips = speechcache.NewPersistent(0, diskDir)
		} else {
			speechClips = speechcache.New(0)
		}
	}
	s := &Server{
		config:                 cfg,
		llmClient:              llmClient,
		toolManager:            toolManager,
		sessionManager:         sessionManager,
		store:                  store,
		port:                   port,
		portReady:              make(chan int, 1),
		speechClips:            speechClips,
		runParentCtx:           context.Background(),
		activeRuns:             make(map[string]map[string]context.CancelFunc),
		sessionEventSubs:       make(map[string]map[chan ChatStreamEvent]struct{}),
		serialQueueWorkers:     make(map[string]struct{}),
		chromeExtensionBridge:  newChromeExtensionBridge(),
		contextCompressor:      contextcompress.NewCompressorWithSessionStore(contextcompress.Config{Enabled: true}, sessionManager),
		claudeHealthCache:      newClaudeHealthCache(),
		approvalResolvePayload: make(map[string]approvalResolvePayload),
		mcpBridge:              newMCPBridgeState(),
		writeTaskSource:        os.WriteFile,
		removeTaskSource:       os.Remove,
	}
	s.initApprovalBroker()
	s.dockerRuntime = newDockerRuntimeManager(s)

	integrationtools.Register(s.toolManager, store, speechClips, sessionManager)

	if customStore, ok := store.(storage.CustomEnvStore); ok {
		if customEnv, err := customStore.GetCustomEnv(); err == nil {
			runtimeenv.MergeCustomEnv(customEnv)
		}
	}

	if settings, err := store.GetSettings(); err == nil {
		filesearch.SetIndexingEnabledFromSettings(settings)
		folder := strings.TrimSpace(settings[sessionsFolderSettingKey])
		if folder == "" {
			folder = filepath.Join(cfg.DataPath, "sessions")
		}
		sessionManager.SetJSONLFolder(folder)
	}

	s.registerServerBackedTools(s.toolManager)
	s.bootstrapDisabledToolsByDefault()
	s.setupRoutes()
	return s
}

// Port returns the bound HTTP port. When configured with port 0, this is updated
// after Run successfully binds a random available port.
func (s *Server) Port() int {
	s.portMu.RLock()
	defer s.portMu.RUnlock()
	return s.port
}

// PortReady yields the actual HTTP port once Run successfully binds the listener.
func (s *Server) PortReady() <-chan int {
	return s.portReady
}

// Run starts the HTTP server
func (s *Server) Run(ctx context.Context) error {

	if ctx != nil {
		s.runParentCtx = ctx
	}
	addr := fmt.Sprintf("0.0.0.0:%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		s.portMu.Lock()
		s.port = tcpAddr.Port
		s.portMu.Unlock()
	}
	select {
	case s.portReady <- s.Port():
	default:
	}
	close(s.portReady)
	logging.Info("Starting HTTP server on %s", listener.Addr().String())
	fmt.Printf("HTTP API server running on http://0.0.0.0:%d (accessible from any host)\n", s.Port())

	go s.runTelegramDuplexLoop(ctx)
	go s.runA2ATunnelIfConfigured()
	go s.dockerRuntime.runIdleReaper(ctx)
	go s.resumeSerialSessionQueues()

	server := &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	go func() {
		<-ctx.Done()
		logging.Info("Shutting down HTTP server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	return server.Serve(listener)
}

func (s *Server) sessionRunParentContext() context.Context {
	if s == nil || s.runParentCtx == nil {
		return context.Background()
	}
	return s.runParentCtx
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	agentName := defaultAgentName
	if settings, err := s.store.GetSettings(); err == nil {
		if v := strings.TrimSpace(settings[agentNameSettingKey]); v != "" {
			agentName = v
		}
	}
	dockerSafeMode := strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != ""
	containerized := dockerSafeMode || isRunningInContainer()
	statusCode := http.StatusOK
	body := map[string]any{
		"status":           "ok",
		"agent_name":       agentName,
		"docker_safe_mode": dockerSafeMode,
		"containerized":    containerized,
	}
	activeProvider := ""
	if s.config != nil {
		activeProvider = config.NormalizeProviderRef(s.config.ActiveProvider)
		if activeProvider != "" {
			body["provider"] = activeProvider
			if model := s.resolveModelForProvider(config.ProviderType(activeProvider)); strings.TrimSpace(model) != "" {
				body["model"] = strings.TrimSpace(model)
			}
		}
	}
	if dockerSafeMode && activeProvider != "" {
		providerType := config.ProviderType(activeProvider)
		if config.GetProviderDefinitionForRef(activeProvider) != nil {
			usage := s.providerUsageStatus(r.Context(), providerType)
			body["provider_usage"] = usage
			if limitReached, detail := providerUsageLimitReached(usage, time.Now()); limitReached {
				statusCode = http.StatusServiceUnavailable
				body["status"] = "offline"
				body["reason"] = providerUsageLimitHealthReason(activeProvider)
				body["message"] = providerUsageLimitHealthMessage(usage, detail)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(body)
}

func providerUsageLimitHealthReason(provider string) string {
	provider = config.NormalizeProviderRef(provider)
	if provider == "" {
		provider = "provider"
	}
	return provider + "_usage_limit_reached"
}

func providerUsageLimitHealthMessage(usage ProviderUsageResponse, detail string) string {
	providerName := strings.TrimSpace(usage.Provider)
	if providerName == "" {
		providerName = "Provider"
	}
	message := providerName + " usage limit reached; this container stays offline until usage resets."
	if detail = strings.TrimSpace(detail); detail != "" {
		message += " " + detail
	}
	return message
}

func isRunningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	data, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	text := strings.ToLower(string(data))
	return strings.Contains(text, "docker") ||
		strings.Contains(text, "containerd") ||
		strings.Contains(text, "kubepods")
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
