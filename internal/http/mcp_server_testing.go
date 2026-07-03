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

	"github.com/A2gent/brute/internal/storage"
)

// MCP transport testing lives here so mcp_servers.go stays focused on request
// validation and HTTP handlers; behavior is unchanged by this package-level split.
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

	initResp, err := requestMCPHTTPRPC(ctx, client, cfg, collector, "initialize", 1, map[string]interface{}{
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

	if _, err := requestMCPHTTPRPC(ctx, client, cfg, collector, "notifications/initialized", nil, map[string]interface{}{}); err != nil {
		collector.add("initialized notification returned an error: %v", err)
	}

	toolsResp, err := requestMCPHTTPRPC(ctx, client, cfg, collector, "tools/list", 2, map[string]interface{}{})
	if err != nil {
		return nil, nil, nil, err
	}

	initResult := mapFromAny(initResp["result"])
	serverInfo := mapFromAny(initResult["serverInfo"])
	capabilities := mapFromAny(initResult["capabilities"])
	tools := mcpToolsFromToolsListResult(mapFromAny(toolsResp["result"]))
	return serverInfo, capabilities, tools, nil
}

func requestMCPHTTPRPC(ctx context.Context, client *http.Client, cfg *mcpServerConfig, collector *mcpLogCollector, method string, id interface{}, params interface{}) (map[string]interface{}, error) {
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

func (s *Server) listMCPServerTools(parent context.Context, server *storage.MCPServer) ([]MCPToolResponse, error) {
	cfg, err := s.resolveMCPServerRuntimeConfig(server)
	if err != nil {
		return nil, err
	}
	result := s.testMCPServer(parent, cfg)
	if !result.Success {
		return nil, fmt.Errorf("%s", result.Message)
	}
	return result.Tools, nil
}

func (s *Server) callMCPServerTool(parent context.Context, server *storage.MCPServer, toolName string, args map[string]interface{}) (map[string]interface{}, error) {
	cfg, err := s.resolveMCPServerRuntimeConfig(server)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(parent, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	collector := &mcpLogCollector{}
	var result map[string]interface{}
	switch cfg.Transport {
	case mcpTransportHTTP:
		result, err = s.callMCPHTTPTool(ctx, cfg, collector, toolName, args)
	case mcpTransportStdio:
		result, err = s.callMCPStdioTool(ctx, cfg, collector, toolName, args)
	default:
		err = fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}
	if err != nil {
		if logs := collector.list(); len(logs) > 0 {
			return nil, fmt.Errorf("%w; logs: %s", err, strings.Join(logs, "\n"))
		}
		return nil, err
	}
	return result, nil
}

func (s *Server) callMCPHTTPTool(ctx context.Context, cfg *mcpServerConfig, collector *mcpLogCollector, toolName string, args map[string]interface{}) (map[string]interface{}, error) {
	client := &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	if _, err := requestMCPHTTPRPC(ctx, client, cfg, collector, "initialize", 1, map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "aagent",
			"version": "1.0.0",
		},
	}); err != nil {
		return nil, err
	}

	if _, err := requestMCPHTTPRPC(ctx, client, cfg, collector, "notifications/initialized", nil, map[string]interface{}{}); err != nil {
		collector.add("initialized notification returned an error: %v", err)
	}

	callResp, err := requestMCPHTTPRPC(ctx, client, cfg, collector, "tools/call", 2, map[string]interface{}{
		"name":      toolName,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}
	return mapFromAny(callResp["result"]), nil
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

func (s *Server) callMCPStdioTool(ctx context.Context, cfg *mcpServerConfig, collector *mcpLogCollector, toolName string, args map[string]interface{}) (map[string]interface{}, error) {
	result, err := s.callMCPStdioToolOnce(ctx, cfg, collector, toolName, args, true)
	if err == nil {
		return result, nil
	}

	logText := strings.ToLower(strings.Join(collector.list(), "\n"))
	if strings.Contains(logText, "invalid json") && strings.Contains(logText, "content-length") {
		collector.add("detected line-delimited JSON stdio server; retrying tools/call without Content-Length framing")
		return s.callMCPStdioToolOnce(ctx, cfg, collector, toolName, args, false)
	}
	return nil, err
}

func (s *Server) callMCPStdioToolOnce(ctx context.Context, cfg *mcpServerConfig, collector *mcpLogCollector, toolName string, args map[string]interface{}, useFraming bool) (map[string]interface{}, error) {
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
		return nil, fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stderr pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start MCP server command: %w", err)
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
		return nil, fmt.Errorf("failed to send initialize: %w", err)
	}

	if _, err := awaitResponse("1"); err != nil {
		return nil, err
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
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to send tools/call: %w", err)
	}

	callResp, err := awaitResponse("2")
	if err != nil {
		return nil, err
	}
	return mapFromAny(callResp["result"]), nil
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
