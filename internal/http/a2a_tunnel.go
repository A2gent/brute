package http

// a2a_tunnel.go — wires the A2A gRPC tunnel client into the HTTP server.
//
// Lifecycle:
//   - On Server.Run: if a2_registry integration exists and is enabled, start the tunnel.
//   - On PUT /integrations/{id}: if it is a2_registry, reconcile tunnel state.
//   - On DELETE /integrations/{id}: stop the tunnel if it was a2_registry.
//
// Endpoints added to /integrations:
//   GET /integrations/a2_registry/tunnel-status         — JSON snapshot
//   GET /integrations/a2_registry/tunnel-status/stream  — SSE live log

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/a2atunnel"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

const (
	a2aInboundProjectIDSettingKey = "A2A_INBOUND_PROJECT_ID"
	a2aOutboundSessionKey         = "a2a_outbound"
	a2aOutboundTargetAgentIDKey   = "a2a_target_agent_id"
	a2aOutboundTargetAgentNameKey = "a2a_target_agent_name"
	a2aConversationIDKey          = "a2a_conversation_id"
)

// tunnelState holds all mutable state for the running tunnel goroutine.
type tunnelState struct {
	client *a2atunnel.TunnelClient
	cancel context.CancelFunc
}

// a2aTunnel is the singleton tunnel controller on the server.
type a2aTunnel struct {
	mu      sync.Mutex
	current *tunnelState
}

// a2aTunnel is embedded in Server (added below via the server field).
// We keep it as a separate struct to isolate the locking.

// startA2ATunnel starts the gRPC tunnel using the current a2_registry
// integration config. If a tunnel is already running it is stopped first.
func (s *Server) startA2ATunnel(apiKey, transport, squareAddr string) {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()

	// Stop any existing tunnel first.
	s.stopA2ATunnelLocked()

	if apiKey == "" || squareAddr == "" {
		logging.Warn("A2A tunnel: missing api_key or tunnel address — not starting")
		return
	}
	if transport == "" {
		transport = string(a2atunnel.TransportGRPC)
	}

	handler := a2atunnel.NewInboundHandler(
		"brute",
		s.sessionManager,
		s.makeA2AAgentFactory(),
		s.toolManagerForSession,
		s.getA2AInboundProjectID,
	)

	client := a2atunnel.NewWithTransport(squareAddr, apiKey, handler, a2atunnel.Transport(transport))

	ctx, cancel := context.WithCancel(context.Background())
	s.tunnelClient = client
	s.tunnelCancel = cancel

	go client.Run(ctx)
	logging.Info("A2A tunnel started: transport=%s addr=%s", transport, squareAddr)
}

// stopA2ATunnel stops the running tunnel (if any). Safe to call when idle.
func (s *Server) stopA2ATunnel() {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	s.stopA2ATunnelLocked()
}

// stopA2ATunnelLocked must be called with tunnelMu held.
func (s *Server) stopA2ATunnelLocked() {
	if s.tunnelCancel != nil {
		s.tunnelCancel()
		s.tunnelCancel = nil
		s.tunnelClient = nil
		logging.Info("A2A tunnel stopped")
	}
}

// runA2ATunnelIfConfigured reads the a2_registry integration from the store
// and starts the tunnel if it is enabled and configured. Called at startup
// and whenever the integration is mutated.
func (s *Server) runA2ATunnelIfConfigured() {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		logging.Warn("A2A tunnel: failed to list integrations: %v", err)
		return
	}
	for _, integ := range integrations {
		if integ == nil || integ.Provider != "a2_registry" {
			continue
		}
		if !integ.Enabled {
			s.stopA2ATunnel()
			return
		}
		apiKey := strings.TrimSpace(integ.Config["api_key"])
		transport := strings.TrimSpace(strings.ToLower(integ.Config["transport"]))
		if transport == "" {
			transport = string(a2atunnel.TransportGRPC)
		}
		squareAddr := strings.TrimSpace(integ.Config["square_grpc_addr"])
		if transport == string(a2atunnel.TransportWebSocket) {
			squareAddr = strings.TrimSpace(integ.Config["square_ws_url"])
		}
		s.startA2ATunnel(apiKey, transport, squareAddr)
		return
	}
	// No integration found — ensure tunnel is stopped.
	s.stopA2ATunnel()
}

// getA2AInboundProjectID returns the configured project ID for inbound A2A
// sessions, or "" if none is set. Called dynamically per-request.
func (s *Server) getA2AInboundProjectID() string {
	settings, err := s.store.GetSettings()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(settings[a2aInboundProjectIDSettingKey])
}

// makeA2AAgentFactory returns a factory that constructs an *agent.Agent
// using the server's active LLM client and current config.
func (s *Server) makeA2AAgentFactory() a2atunnel.AgentRunnerFactory {
	return func(toolManager *tools.Manager) *agent.Agent {
		providerType := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
		model := s.resolveModelForProvider(providerType)
		// Use the server's shared llmClient — already handles provider routing.
		cfg := agent.Config{
			Name:         "brute-a2a",
			Model:        model,
			SystemPrompt: s.buildSystemPromptForA2A(),
			MaxSteps:     s.config.MaxSteps,
			Temperature:  s.config.Temperature,
		}
		return agent.New(cfg, s.llmClient, toolManager, s.sessionManager)
	}
}

// buildSystemPromptForA2A builds a system prompt for inbound A2A sessions.
// Falls back to the default agent system prompt.
func (s *Server) buildSystemPromptForA2A() string {
	settings, err := s.store.GetSettings()
	if err == nil {
		if p := strings.TrimSpace(settings["AAGENT_SYSTEM_PROMPT"]); p != "" {
			return p
		}
	}
	return agent.DefaultSystemPrompt()
}

// ---- HTTP handlers for tunnel status ----

// handleA2ATunnelStatus returns a JSON snapshot of the current tunnel state.
func (s *Server) handleA2ATunnelStatus(w http.ResponseWriter, r *http.Request) {
	s.tunnelMu.Lock()
	client := s.tunnelClient
	s.tunnelMu.Unlock()

	if client == nil {
		s.jsonResponse(w, http.StatusOK, a2atunnel.Status{
			State:      a2atunnel.StateDisconnected,
			SquareAddr: "",
			Log:        nil,
		})
		return
	}
	s.jsonResponse(w, http.StatusOK, client.Status())
}

// handleA2ATunnelStatusStream streams live log entries as Server-Sent Events.
// The client receives an event for each new log line and a periodic keepalive.
//
// Event format:
//
//	data: {"time":"...","message":"..."}
func (s *Server) handleA2ATunnelStatusStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	// Helper to write one SSE event.
	writeEvent := func(data interface{}) bool {
		b, err := json.Marshal(data)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "data: %s\n\n", b)
		if err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Send a comment keepalive so the client knows the stream is alive.
	writeKeepalive := func() bool {
		_, err := fmt.Fprintf(w, ": keepalive\n\n")
		if err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Flush current log snapshot immediately.
	s.tunnelMu.Lock()
	client := s.tunnelClient
	s.tunnelMu.Unlock()

	if client != nil {
		for _, entry := range client.Status().Log {
			if !writeEvent(entry) {
				return
			}
		}
	}

	// Subscribe to new log entries.
	var logCh chan struct{}
	if client != nil {
		logCh = client.SubscribeLog()
		defer client.UnsubscribeLog(logCh)
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	// We also need to re-check the client pointer periodically, because the
	// tunnel may start/stop after the SSE stream was opened.
	refresh := time.NewTicker(2 * time.Second)
	defer refresh.Stop()

	lastLogCount := 0
	if client != nil {
		lastLogCount = len(client.Status().Log)
	}

	for {
		select {
		case <-r.Context().Done():
			return

		case <-keepalive.C:
			if !writeKeepalive() {
				return
			}

		case <-logCh:
			// New log entry on the current client.
			s.tunnelMu.Lock()
			c := s.tunnelClient
			s.tunnelMu.Unlock()
			if c == nil {
				continue
			}
			entries := c.Status().Log
			for i := lastLogCount; i < len(entries); i++ {
				if !writeEvent(entries[i]) {
					return
				}
			}
			lastLogCount = len(entries)

		case <-refresh.C:
			// Check if the client changed (tunnel restarted).
			s.tunnelMu.Lock()
			newClient := s.tunnelClient
			s.tunnelMu.Unlock()

			if newClient != client {
				// Re-subscribe to the new client.
				if client != nil && logCh != nil {
					client.UnsubscribeLog(logCh)
				}
				client = newClient
				logCh = nil
				lastLogCount = 0
				if client != nil {
					logCh = client.SubscribeLog()
					for _, entry := range client.Status().Log {
						if !writeEvent(entry) {
							return
						}
					}
					lastLogCount = len(client.Status().Log)
				}
			}
		}
	}
}

// reconcileA2ATunnelAfterIntegrationSave is called after any integration
// create/update/delete that touches the a2_registry provider. It re-evaluates
// whether the tunnel should be running.
func (s *Server) reconcileA2ATunnelAfterIntegrationSave(provider string) {
	if provider == "a2_registry" {
		go s.runA2ATunnelIfConfigured()
	}
}

// buildSessionWithA2AMeta is used by the session list handler to expose
// A2A inbound metadata fields in the response.
func sessionA2AMeta(sess *session.Session) (isInbound bool, sourceAgentID, sourceAgentName string) {
	if sess.Metadata == nil {
		return
	}
	if v, ok := sess.Metadata[a2atunnel.MetaA2AInbound]; ok {
		if b, ok := v.(bool); ok && b {
			isInbound = true
		}
	}
	if v, ok := sess.Metadata[a2atunnel.MetaA2ASourceAgentID]; ok {
		sourceAgentID, _ = v.(string)
	}
	if v, ok := sess.Metadata[a2atunnel.MetaA2ASourceAgentName]; ok {
		sourceAgentName, _ = v.(string)
	}
	return
}

func sessionA2AOutboundMeta(sess *session.Session) (isOutbound bool, targetAgentID, targetAgentName string) {
	if sess == nil || sess.Metadata == nil {
		return
	}
	if v, ok := sess.Metadata[a2aOutboundSessionKey]; ok {
		isOutbound, _ = v.(bool)
	}
	if v, ok := sess.Metadata[a2aOutboundTargetAgentIDKey]; ok {
		targetAgentID, _ = v.(string)
	}
	if v, ok := sess.Metadata[a2aOutboundTargetAgentNameKey]; ok {
		targetAgentName, _ = v.(string)
	}
	return
}
