package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/A2gent/brute/internal/storage"
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

type mcpLogCollector struct {
	mu   sync.Mutex
	logs []string
}

func (c *mcpLogCollector) add(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.logs) >= mcpMaxCapturedLogLines {
		copy(c.logs, c.logs[1:])
		c.logs[len(c.logs)-1] = line
		return
	}
	c.logs = append(c.logs, line)
}

func (c *mcpLogCollector) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.logs))
	copy(out, c.logs)
	return out
}

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListMCPServers()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list MCP servers: "+err.Error())
		return
	}

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

	next.ID = existing.ID
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

	cfg, err := decodeMCPServerConfig(server)
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
		Name:      cfg.Name,
		Transport: cfg.Transport,
		Enabled:   cfg.Enabled,
		Config:    encodeMCPServerConfig(cfg),
	}, nil
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

func mcpServerToResponse(server *storage.MCPServer) MCPServerResponse {
	cfg, err := decodeMCPServerConfig(server)
	if err != nil {
		return MCPServerResponse{
			ID:                  server.ID,
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

func (s *Server) testMCPServer(parent context.Context, cfg *mcpServerConfig) *MCPServerTestResponse {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	collector := &mcpLogCollector{}

	var (
		serverInfo   map[string]interface{}
		capabilities map[string]interface{}
		tools        []MCPToolResponse
		err          error
	)

	switch cfg.Transport {
	case mcpTransportStdio:
		serverInfo, capabilities, tools, err = s.testMCPStdio(ctx, cfg, collector)
	case mcpTransportHTTP:
		serverInfo, capabilities, tools, err = s.testMCPHTTP(ctx, cfg, collector)
	default:
		err = fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}

	metadataPayload := map[string]interface{}{
		"server_info":  serverInfo,
		"capabilities": capabilities,
	}
	metadataJSON, _ := json.Marshal(metadataPayload)
	metadataTokens := estimateTokensApprox(string(metadataJSON))
	toolsJSON, _ := json.Marshal(tools)
	toolsTokens := estimateTokensApprox(string(toolsJSON))

	durationMs := time.Since(start).Milliseconds()
	logs := collector.list()

	if err != nil {
		message := "MCP server test failed: " + err.Error()
		errText := strings.ToLower(err.Error())
		if strings.Contains(errText, "timeout while waiting for init-1 response") || strings.Contains(errText, "timeout while waiting for 1 response") {
			message += ". This often means the command is still downloading/installing on first run. Increase timeout or pre-install the MCP package."
		}
		logText := strings.ToLower(strings.Join(logs, "\n"))
		if strings.Contains(logText, "content-length:: command not found") || strings.Contains(logText, "id:init-1: command not found") {
			message += " It also looks like command/args were parsed by a shell instead of MCP. Use command `npx` and args on separate lines: `-y` and `chrome-devtools-mcp@latest`."
		}
		if strings.Contains(logText, "google collects usage statistics") && (strings.Contains(errText, "timeout while waiting for init-1 response") || strings.Contains(errText, "timeout while waiting for 1 response")) {
			message += " Chrome DevTools MCP started but did not complete handshake. Try adding arg `--no-usage-statistics`, and prefer a direct install (`npm i -g chrome-devtools-mcp` then command `chrome-devtools-mcp`) instead of `npx`."
		}
		if strings.Contains(logText, "invalid json") && strings.Contains(logText, "content-length") {
			message += " This MCP server appears to expect line-delimited JSON on stdio. The tester retries automatically, but startup/download latency can still cause a timeout."
		}
		return &MCPServerTestResponse{
			Success:                 false,
			Message:                 message,
			Transport:               cfg.Transport,
			DurationMs:              durationMs,
			ServerInfo:              serverInfo,
			Capabilities:            capabilities,
			Tools:                   []MCPToolResponse{},
			ToolCount:               0,
			EstimatedTokens:         metadataTokens + toolsTokens,
			EstimatedMetadataTokens: metadataTokens,
			EstimatedToolsTokens:    toolsTokens,
			Logs:                    logs,
		}
	}

	message := fmt.Sprintf("MCP server test succeeded. Found %d tools.", len(tools))
	if len(tools) == 0 {
		message = "MCP server test succeeded. No tools were exposed by tools/list."
	}
	return &MCPServerTestResponse{
		Success:                 true,
		Message:                 message,
		Transport:               cfg.Transport,
		DurationMs:              durationMs,
		ServerInfo:              serverInfo,
		Capabilities:            capabilities,
		Tools:                   tools,
		ToolCount:               len(tools),
		EstimatedTokens:         metadataTokens + toolsTokens,
		EstimatedMetadataTokens: metadataTokens,
		EstimatedToolsTokens:    toolsTokens,
		Logs:                    logs,
	}
}

func (s *Server) testMCPHTTP(ctx context.Context, cfg *mcpServerConfig, collector *mcpLogCollector) (map[string]interface{}, map[string]interface{}, []MCPToolResponse, error) {
	client := &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	requestRPC := func(method string, id interface{}, params interface{}) (map[string]interface{}, error) {
		payload := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  method,
		}
		if id != nil {
			payload["id"] = id
		}
		if params != nil {
			payload["params"] = params
		}
		body, _ := json.Marshal(payload)
		collector.add("http > %s", string(body))

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		for key, value := range cfg.Headers {
			req.Header.Set(key, value)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		collector.add("http < status=%d", resp.StatusCode)
		collector.add("http < %s", strings.TrimSpace(string(respBody)))
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, fmt.Errorf("HTTP MCP request %q failed with status %d", method, resp.StatusCode)
		}

		var out map[string]interface{}
		if err := json.Unmarshal(respBody, &out); err != nil {
			return nil, fmt.Errorf("failed to decode MCP response for %q: %w", method, err)
		}
		if rpcErr, ok := out["error"].(map[string]interface{}); ok && len(rpcErr) > 0 {
			return nil, fmt.Errorf("MCP error for %q: %v", method, rpcErr)
		}
		return out, nil
	}

	initResp, err := requestRPC("initialize", 1, map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "aagent",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return nil, nil, nil, err
	}

	if _, err := requestRPC("notifications/initialized", nil, map[string]interface{}{}); err != nil {
		collector.add("initialized notification returned an error: %v", err)
	}

	toolsResp, err := requestRPC("tools/list", 2, map[string]interface{}{})
	if err != nil {
		return nil, nil, nil, err
	}

	initResult := mapFromAny(initResp["result"])
	serverInfo := mapFromAny(initResult["serverInfo"])
	capabilities := mapFromAny(initResult["capabilities"])
	tools := mcpToolsFromToolsListResult(mapFromAny(toolsResp["result"]))
	return serverInfo, capabilities, tools, nil
}

func (s *Server) testMCPStdio(ctx context.Context, cfg *mcpServerConfig, collector *mcpLogCollector) (map[string]interface{}, map[string]interface{}, []MCPToolResponse, error) {
	serverInfo, capabilities, tools, err := s.testMCPStdioOnce(ctx, cfg, collector, true)
	if err == nil {
		return serverInfo, capabilities, tools, nil
	}

	logText := strings.ToLower(strings.Join(collector.list(), "\n"))
	if strings.Contains(logText, "invalid json") && strings.Contains(logText, "content-length") {
		collector.add("detected line-delimited JSON stdio server; retrying initialize/tools without Content-Length framing")
		return s.testMCPStdioOnce(ctx, cfg, collector, false)
	}
	return nil, nil, nil, err
}

func (s *Server) testMCPStdioOnce(ctx context.Context, cfg *mcpServerConfig, collector *mcpLogCollector, useFraming bool) (map[string]interface{}, map[string]interface{}, []MCPToolResponse, error) {
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	cmd.Env = append([]string{}, os.Environ()...)
	if len(cfg.Env) > 0 {
		keys := make([]string, 0, len(cfg.Env))
		for key := range cfg.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			cmd.Env = append(cmd.Env, key+"="+cfg.Env[key])
		}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open stderr pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to start MCP server command: %w", err)
	}
	argsJSON, _ := json.Marshal(cfg.Args)
	collector.add("started stdio command: command=%q args=%s framing=%t", cfg.Command, string(argsJSON), useFraming)

	errDone := make(chan struct{})
	framingMismatchCh := make(chan struct{}, 1)
	go func() {
		defer close(errDone)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			collector.add("stderr: %s", line)
			lower := strings.ToLower(line)
			if strings.Contains(lower, "invalid json") && strings.Contains(lower, "content-length") {
				select {
				case framingMismatchCh <- struct{}{}:
				default:
				}
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			collector.add("stderr read error: %v", scanErr)
		}
	}()

	msgCh := make(chan map[string]interface{}, 32)
	readErrCh := make(chan error, 1)
	go readMCPMessages(stdout, msgCh, readErrCh, collector)

	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		<-errDone
	}()

	write := func(payload map[string]interface{}) error {
		body, _ := json.Marshal(payload)
		collector.add("rpc > %s", string(body))
		if useFraming {
			return writeMCPFramedMessage(stdin, body)
		}
		return writeMCPLineMessage(stdin, body)
	}

	awaitResponse := func(id string) (map[string]interface{}, error) {
		for {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("timeout while waiting for %s response", id)
			case <-framingMismatchCh:
				return nil, fmt.Errorf("stdio framing mismatch detected")
			case err := <-readErrCh:
				if err == io.EOF {
					return nil, fmt.Errorf("MCP server closed stream while waiting for %s", id)
				}
				return nil, fmt.Errorf("failed to read MCP response: %w", err)
			case msg := <-msgCh:
				raw, _ := json.Marshal(msg)
				collector.add("rpc < %s", string(raw))
				if responseID, ok := msg["id"]; ok {
					if fmt.Sprintf("%v", responseID) == id {
						if rpcErr, ok := msg["error"].(map[string]interface{}); ok && len(rpcErr) > 0 {
							return nil, fmt.Errorf("MCP error in response %s: %v", id, rpcErr)
						}
						return msg, nil
					}
				}
				if method, ok := msg["method"].(string); ok {
					collector.add("notification: %s", method)
				}
			}
		}
	}

	if err := write(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "aagent",
				"version": "1.0.0",
			},
		},
	}); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to send initialize: %w", err)
	}

	initResp, err := awaitResponse("1")
	if err != nil {
		return nil, nil, nil, err
	}

	if err := write(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]interface{}{},
	}); err != nil {
		collector.add("failed to send initialized notification: %v", err)
	}

	if err := write(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]interface{}{},
	}); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to send tools/list: %w", err)
	}

	toolsResp, err := awaitResponse("2")
	if err != nil {
		return nil, nil, nil, err
	}

	initResult := mapFromAny(initResp["result"])
	serverInfo := mapFromAny(initResult["serverInfo"])
	capabilities := mapFromAny(initResult["capabilities"])
	tools := mcpToolsFromToolsListResult(mapFromAny(toolsResp["result"]))
	return serverInfo, capabilities, tools, nil
}

func readMCPMessages(stdout io.Reader, msgCh chan<- map[string]interface{}, errCh chan<- error, collector *mcpLogCollector) {
	reader := bufio.NewReader(stdout)
	for {
		msg, err := readMCPMessage(reader, collector)
		if err != nil {
			errCh <- err
			return
		}
		msgCh <- msg
	}
}

func readMCPMessage(reader *bufio.Reader, collector *mcpLogCollector) (map[string]interface{}, error) {
	for {
		firstLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmedFirst := strings.TrimSpace(firstLine)
		if trimmedFirst == "" {
			continue
		}

		if strings.HasPrefix(strings.ToLower(trimmedFirst), "content-length:") {
			parts := strings.SplitN(trimmedFirst, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid content-length header: %q", trimmedFirst)
			}
			length, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil || length <= 0 {
				return nil, fmt.Errorf("invalid content-length value: %q", trimmedFirst)
			}

			for {
				headerLine, err := reader.ReadString('\n')
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(headerLine) == "" {
					break
				}
			}

			body := make([]byte, length)
			if _, err := io.ReadFull(reader, body); err != nil {
				return nil, err
			}
			var msg map[string]interface{}
			if err := json.Unmarshal(body, &msg); err != nil {
				return nil, fmt.Errorf("invalid json-rpc body: %w", err)
			}
			return msg, nil
		}

		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(trimmedFirst), &msg); err == nil {
			return msg, nil
		}
		collector.add("stdout: %s", trimmedFirst)
	}
}

func writeMCPFramedMessage(writer io.Writer, body []byte) error {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := writer.Write([]byte(header)); err != nil {
		return err
	}
	_, err := writer.Write(body)
	return err
}

func writeMCPLineMessage(writer io.Writer, body []byte) error {
	if _, err := writer.Write(body); err != nil {
		return err
	}
	_, err := writer.Write([]byte("\n"))
	return err
}

func mcpToolsFromToolsListResult(result map[string]interface{}) []MCPToolResponse {
	rawTools, ok := result["tools"].([]interface{})
	if !ok || len(rawTools) == 0 {
		return []MCPToolResponse{}
	}
	out := make([]MCPToolResponse, 0, len(rawTools))
	for _, item := range rawTools {
		toolMap := mapFromAny(item)
		if len(toolMap) == 0 {
			continue
		}
		entry := MCPToolResponse{
			Name:        strings.TrimSpace(asString(toolMap["name"])),
			Description: strings.TrimSpace(asString(toolMap["description"])),
			InputSchema: mapFromAny(toolMap["inputSchema"]),
			Raw:         toolMap,
		}
		out = append(out, entry)
	}
	return out
}

func mapFromAny(value interface{}) map[string]interface{} {
	if value == nil {
		return map[string]interface{}{}
	}
	if out, ok := value.(map[string]interface{}); ok {
		return out
	}
	return map[string]interface{}{}
}

func asString(value interface{}) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}
