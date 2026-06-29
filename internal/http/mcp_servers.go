package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	mcpTransportStdio            = "stdio"
	mcpTransportHTTP             = "http"
	mcpConfigKeyCommand          = "command"
	mcpConfigKeyArgsJSON         = "args_json"
	mcpConfigKeyEnvJSON          = "env_json"
	mcpConfigKeyCwd              = "cwd"
	mcpConfigKeyURL              = "url"
	mcpConfigKeyHeadersJSON      = "headers_json"
	mcpConfigKeyTimeoutSeconds   = "timeout_seconds"
	mcpDefaultTestTimeoutSeconds = 60
	mcpMinTestTimeoutSeconds     = 1
	mcpMaxTestTimeoutSeconds     = 120
	mcpMaxCapturedLogLines       = 200
	mcpProtocolVersion           = "2024-10-07"
)

type MCPServerRequest struct {
	ProjectID      *string           `json:"project_id,omitempty"`
	Name           string            `json:"name"`
	Transport      string            `json:"transport"`
	Enabled        *bool             `json:"enabled,omitempty"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type MCPServerResponse struct {
	ID                  string            `json:"id"`
	ProjectID           string            `json:"project_id,omitempty"`
	Name                string            `json:"name"`
	Transport           string            `json:"transport"`
	Enabled             bool              `json:"enabled"`
	Command             string            `json:"command,omitempty"`
	Args                []string          `json:"args,omitempty"`
	Env                 map[string]string `json:"env,omitempty"`
	Cwd                 string            `json:"cwd,omitempty"`
	URL                 string            `json:"url,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	TimeoutSeconds      int               `json:"timeout_seconds"`
	LastTestAt          *time.Time        `json:"last_test_at,omitempty"`
	LastTestSuccess     *bool             `json:"last_test_success,omitempty"`
	LastTestMessage     string            `json:"last_test_message,omitempty"`
	LastEstimatedTokens *int              `json:"last_estimated_tokens,omitempty"`
	LastToolCount       *int              `json:"last_tool_count,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

type MCPToolResponse struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
	Raw         map[string]interface{} `json:"raw,omitempty"`
}

type MCPServerTestResponse struct {
	Success                 bool                   `json:"success"`
	Message                 string                 `json:"message"`
	Transport               string                 `json:"transport"`
	DurationMs              int64                  `json:"duration_ms"`
	ServerInfo              map[string]interface{} `json:"server_info,omitempty"`
	Capabilities            map[string]interface{} `json:"capabilities,omitempty"`
	Tools                   []MCPToolResponse      `json:"tools"`
	ToolCount               int                    `json:"tool_count"`
	EstimatedTokens         int                    `json:"estimated_tokens"`
	EstimatedMetadataTokens int                    `json:"estimated_metadata_tokens"`
	EstimatedToolsTokens    int                    `json:"estimated_tools_tokens"`
	Logs                    []string               `json:"logs"`
}

type mcpServerConfig struct {
	Name           string
	Transport      string
	Enabled        bool
	Command        string
	Args           []string
	Env            map[string]string
	Cwd            string
	URL            string
	Headers        map[string]string
	TimeoutSeconds int
}

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	projectID, hasProjectFilter := mcpProjectIDFromRequest(r)
	if hasProjectFilter && projectID != "" {
		if _, err := s.store.GetProject(projectID); err != nil {
			s.errorResponse(w, http.StatusNotFound, "Project not found: "+err.Error())
			return
		}
	}

	servers, err := s.store.ListMCPServers()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list MCP servers: "+err.Error())
		return
	}
	servers = filterMCPServersForProject(servers, projectID, hasProjectFilter)

	resp := make([]MCPServerResponse, len(servers))
	for i, server := range servers {
		resp[i] = mcpServerToResponse(server)
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateMCPServer(w http.ResponseWriter, r *http.Request) {
	var req MCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	server, err := newMCPServerFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.validateMCPServerProject(server.ProjectID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now()
	server.ID = uuid.New().String()
	server.CreatedAt = now
	server.UpdatedAt = now

	if err := s.store.SaveMCPServer(server); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save MCP server: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusCreated, mcpServerToResponse(server))
}

func (s *Server) handleGetMCPServer(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	server, err := s.store.GetMCPServer(serverID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "MCP server not found: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, mcpServerToResponse(server))
}

func (s *Server) handleUpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")

	existing, err := s.store.GetMCPServer(serverID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "MCP server not found: "+err.Error())
		return
	}

	var req MCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	next, err := newMCPServerFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.validateMCPServerProject(next.ProjectID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	next.ID = existing.ID
	if req.ProjectID == nil {
		next.ProjectID = existing.ProjectID
	}
	next.CreatedAt = existing.CreatedAt
	next.LastTestAt = existing.LastTestAt
	next.LastTestSuccess = existing.LastTestSuccess
	next.LastTestMessage = existing.LastTestMessage
	next.LastEstimatedTokens = existing.LastEstimatedTokens
	next.LastToolCount = existing.LastToolCount
	next.UpdatedAt = time.Now()

	if err := s.store.SaveMCPServer(next); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update MCP server: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, mcpServerToResponse(next))
}

func (s *Server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	if err := s.store.DeleteMCPServer(serverID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete MCP server: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestMCPServer(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverID")
	server, err := s.store.GetMCPServer(serverID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "MCP server not found: "+err.Error())
		return
	}

	cfg, err := s.resolveMCPServerRuntimeConfig(server)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid MCP server config: "+err.Error())
		return
	}

	result := s.testMCPServer(r.Context(), cfg)
	now := time.Now()
	success := result.Success
	server.LastTestAt = &now
	server.LastTestSuccess = &success
	server.LastTestMessage = result.Message
	server.LastEstimatedTokens = &result.EstimatedTokens
	server.LastToolCount = &result.ToolCount
	server.UpdatedAt = now
	if saveErr := s.store.SaveMCPServer(server); saveErr != nil {
		// Keep test response even if persistence fails.
		result.Logs = append(result.Logs, fmt.Sprintf("warning: failed to persist last test metadata: %v", saveErr))
	}
	s.jsonResponse(w, http.StatusOK, result)
}

func newMCPServerFromRequest(req MCPServerRequest) (*storage.MCPServer, error) {
	cfg, err := validateMCPServerRequest(req)
	if err != nil {
		return nil, err
	}
	return &storage.MCPServer{
		ProjectID: normalizeMCPServerProjectID(req.ProjectID),
		Name:      cfg.Name,
		Transport: cfg.Transport,
		Enabled:   cfg.Enabled,
		Config:    encodeMCPServerConfig(cfg),
	}, nil
}

func mcpProjectIDFromRequest(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	values, ok := r.URL.Query()["project_id"]
	if !ok {
		return "", false
	}
	if len(values) == 0 {
		return "", true
	}
	return strings.TrimSpace(values[0]), true
}

func normalizeMCPServerProjectID(projectID *string) *string {
	if projectID == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*projectID)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func mcpServerProjectID(server *storage.MCPServer) string {
	if server == nil || server.ProjectID == nil {
		return ""
	}
	return strings.TrimSpace(*server.ProjectID)
}

func filterMCPServersForProject(servers []*storage.MCPServer, projectID string, includeGlobal bool) []*storage.MCPServer {
	projectID = strings.TrimSpace(projectID)
	filtered := make([]*storage.MCPServer, 0, len(servers))
	for _, server := range servers {
		serverProjectID := mcpServerProjectID(server)
		if includeGlobal {
			if serverProjectID == "" || serverProjectID == projectID {
				filtered = append(filtered, server)
			}
			continue
		}
		if serverProjectID == "" {
			filtered = append(filtered, server)
		}
	}
	return filtered
}

func (s *Server) validateMCPServerProject(projectID *string) error {
	if projectID == nil || strings.TrimSpace(*projectID) == "" {
		return nil
	}
	if _, err := s.store.GetProject(strings.TrimSpace(*projectID)); err != nil {
		return fmt.Errorf("project not found: %w", err)
	}
	return nil
}

func validateMCPServerRequest(req MCPServerRequest) (*mcpServerConfig, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}

	transport := strings.ToLower(strings.TrimSpace(req.Transport))
	if transport != mcpTransportStdio && transport != mcpTransportHTTP {
		return nil, fmt.Errorf("transport must be one of: stdio, http")
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = mcpDefaultTestTimeoutSeconds
	}
	if timeoutSeconds < mcpMinTestTimeoutSeconds || timeoutSeconds > mcpMaxTestTimeoutSeconds {
		return nil, fmt.Errorf("timeout_seconds must be between %d and %d", mcpMinTestTimeoutSeconds, mcpMaxTestTimeoutSeconds)
	}

	cfg := &mcpServerConfig{
		Name:           name,
		Transport:      transport,
		Enabled:        enabled,
		Args:           compactStrings(req.Args),
		Env:            compactStringMap(req.Env),
		Cwd:            strings.TrimSpace(req.Cwd),
		Headers:        compactStringMap(req.Headers),
		TimeoutSeconds: timeoutSeconds,
	}

	switch transport {
	case mcpTransportStdio:
		cfg.Command = strings.TrimSpace(req.Command)
		// Accept pasted full command lines in the command field by splitting when args are empty.
		if len(cfg.Args) == 0 && strings.ContainsAny(cfg.Command, " \t") {
			parts := strings.Fields(cfg.Command)
			if len(parts) > 0 {
				cfg.Command = parts[0]
				cfg.Args = append(cfg.Args, parts[1:]...)
			}
		}
		if cfg.Command == "" {
			return nil, fmt.Errorf("command is required for stdio transport")
		}
	case mcpTransportHTTP:
		cfg.URL = strings.TrimSpace(req.URL)
		if cfg.URL == "" {
			return nil, fmt.Errorf("url is required for http transport")
		}
	}

	return cfg, nil
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func compactStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(value)
	}
	return out
}

func encodeMCPServerConfig(cfg *mcpServerConfig) map[string]string {
	config := map[string]string{
		mcpConfigKeyTimeoutSeconds: strconv.Itoa(cfg.TimeoutSeconds),
	}
	if cfg.Command != "" {
		config[mcpConfigKeyCommand] = cfg.Command
	}
	if len(cfg.Args) > 0 {
		if data, err := json.Marshal(cfg.Args); err == nil {
			config[mcpConfigKeyArgsJSON] = string(data)
		}
	}
	if len(cfg.Env) > 0 {
		if data, err := json.Marshal(cfg.Env); err == nil {
			config[mcpConfigKeyEnvJSON] = string(data)
		}
	}
	if cfg.Cwd != "" {
		config[mcpConfigKeyCwd] = cfg.Cwd
	}
	if cfg.URL != "" {
		config[mcpConfigKeyURL] = cfg.URL
	}
	if len(cfg.Headers) > 0 {
		if data, err := json.Marshal(cfg.Headers); err == nil {
			config[mcpConfigKeyHeadersJSON] = string(data)
		}
	}
	return config
}

func decodeMCPServerConfig(server *storage.MCPServer) (*mcpServerConfig, error) {
	if server == nil {
		return nil, fmt.Errorf("missing server")
	}
	cfg := &mcpServerConfig{
		Name:      strings.TrimSpace(server.Name),
		Transport: strings.ToLower(strings.TrimSpace(server.Transport)),
		Enabled:   server.Enabled,
		Env:       map[string]string{},
		Headers:   map[string]string{},
	}

	if cfg.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if cfg.Transport != mcpTransportStdio && cfg.Transport != mcpTransportHTTP {
		return nil, fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}

	cfg.Command = strings.TrimSpace(server.Config[mcpConfigKeyCommand])
	cfg.Cwd = strings.TrimSpace(server.Config[mcpConfigKeyCwd])
	cfg.URL = strings.TrimSpace(server.Config[mcpConfigKeyURL])

	if raw := strings.TrimSpace(server.Config[mcpConfigKeyArgsJSON]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Args); err != nil {
			return nil, fmt.Errorf("invalid args_json: %w", err)
		}
	}
	if raw := strings.TrimSpace(server.Config[mcpConfigKeyEnvJSON]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Env); err != nil {
			return nil, fmt.Errorf("invalid env_json: %w", err)
		}
	}
	if raw := strings.TrimSpace(server.Config[mcpConfigKeyHeadersJSON]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Headers); err != nil {
			return nil, fmt.Errorf("invalid headers_json: %w", err)
		}
	}

	cfg.TimeoutSeconds = mcpDefaultTestTimeoutSeconds
	if raw := strings.TrimSpace(server.Config[mcpConfigKeyTimeoutSeconds]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout_seconds: %w", err)
		}
		cfg.TimeoutSeconds = n
	}
	if cfg.TimeoutSeconds < mcpMinTestTimeoutSeconds || cfg.TimeoutSeconds > mcpMaxTestTimeoutSeconds {
		return nil, fmt.Errorf("timeout_seconds must be between %d and %d", mcpMinTestTimeoutSeconds, mcpMaxTestTimeoutSeconds)
	}

	if cfg.Transport == mcpTransportStdio && cfg.Command == "" {
		return nil, fmt.Errorf("command is required for stdio transport")
	}
	if cfg.Transport == mcpTransportHTTP && cfg.URL == "" {
		return nil, fmt.Errorf("url is required for http transport")
	}

	cfg.Args = compactStrings(cfg.Args)
	cfg.Env = compactStringMap(cfg.Env)
	cfg.Headers = compactStringMap(cfg.Headers)
	return cfg, nil
}

func (s *Server) resolveMCPServerRuntimeConfig(server *storage.MCPServer) (*mcpServerConfig, error) {
	cfg, err := decodeMCPServerConfig(server)
	if err != nil {
		return nil, err
	}
	projectID := mcpServerProjectID(server)
	if projectID == "" || cfg.Transport != mcpTransportStdio {
		return cfg, nil
	}
	projectRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		return nil, fmt.Errorf("project MCP working directory: %w", err)
	}
	cfg.Cwd = projectRoot
	return cfg, nil
}

func mcpServerToResponse(server *storage.MCPServer) MCPServerResponse {
	cfg, err := decodeMCPServerConfig(server)
	if err != nil {
		return MCPServerResponse{
			ID:                  server.ID,
			ProjectID:           mcpServerProjectID(server),
			Name:                server.Name,
			Transport:           server.Transport,
			Enabled:             server.Enabled,
			LastTestAt:          server.LastTestAt,
			LastTestSuccess:     server.LastTestSuccess,
			LastTestMessage:     server.LastTestMessage,
			LastEstimatedTokens: server.LastEstimatedTokens,
			LastToolCount:       server.LastToolCount,
			CreatedAt:           server.CreatedAt,
			UpdatedAt:           server.UpdatedAt,
		}
	}
	return MCPServerResponse{
		ID:                  server.ID,
		ProjectID:           mcpServerProjectID(server),
		Name:                cfg.Name,
		Transport:           cfg.Transport,
		Enabled:             cfg.Enabled,
		Command:             cfg.Command,
		Args:                cfg.Args,
		Env:                 cfg.Env,
		Cwd:                 cfg.Cwd,
		URL:                 cfg.URL,
		Headers:             cfg.Headers,
		TimeoutSeconds:      cfg.TimeoutSeconds,
		LastTestAt:          server.LastTestAt,
		LastTestSuccess:     server.LastTestSuccess,
		LastTestMessage:     server.LastTestMessage,
		LastEstimatedTokens: server.LastEstimatedTokens,
		LastToolCount:       server.LastToolCount,
		CreatedAt:           server.CreatedAt,
		UpdatedAt:           server.UpdatedAt,
	}
}
