// server_core.go keeps the server lifecycle pieces split out of the former server.go without changing HTTP behavior.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/a2atunnel"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-chi/chi/v5"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Server represents the HTTP API server
type Server struct {
	config                *config.Config
	llmClient             llm.Client
	toolManager           *tools.Manager
	sessionManager        *session.Manager
	store                 storage.Store
	router                chi.Router
	port                  int
	portMu                sync.RWMutex
	portReady             chan int
	speechClips           *speechcache.Store
	runParentCtx          context.Context
	activeRunsMu          sync.Mutex
	activeRuns            map[string]map[string]context.CancelFunc
	chromeExtensionBridge *chromeExtensionBridge
	dockerRuntime         *dockerRuntimeManager

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
		config:                cfg,
		llmClient:             llmClient,
		toolManager:           toolManager,
		sessionManager:        sessionManager,
		store:                 store,
		port:                  port,
		portReady:             make(chan int, 1),
		runParentCtx:          context.Background(),
		activeRuns:            make(map[string]map[string]context.CancelFunc),
		chromeExtensionBridge: newChromeExtensionBridge(),
	}
	s.dockerRuntime = newDockerRuntimeManager(s)

	if settings, err := store.GetSettings(); err == nil {
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":           "ok",
		"agent_name":       agentName,
		"docker_safe_mode": dockerSafeMode,
		"containerized":    containerized,
	})
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
