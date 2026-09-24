package cursorcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

const (
	// cursorMCPPluginName is the Cursor plugin id. Cursor namespaces plugin MCP
	// servers as plugin-<plugin>-<server>, so the model-facing id is
	// plugin-a2gent-bridge-a2gent when the server key is "a2gent".
	cursorMCPPluginName = "a2gent-bridge"
	cursorMCPPluginID   = "plugin-a2gent-bridge-a2gent"
	cursorMCPToolNote   = "A2gent tools that have no Cursor native equivalent (question, tasks, suggest_session, suggest_git_commit, session_task_progress, and configured integrations) are exposed by the MCP server a2gent, plugin id plugin-a2gent-bridge-a2gent. Discover them with Cursor's MCP tool listing and call them through Cursor's MCP interface."
)

// MCPBridgeHook builds the per-invocation MCP bridge configuration for a
// session-scoped CLI run. It returns the same JSON shape Claude CLI accepts
// via --mcp-config, plus a revoke callback tied to the CLI process lifetime.
// An empty configJSON disables the bridge for that invocation.
type MCPBridgeHook func(ctx context.Context, sessionID string) (configJSON string, revoke func(), err error)

// mcpBridgeInvocation carries the per-run Cursor CLI args and the cleanup
// callback that must run when the subprocess exits.
type mcpBridgeInvocation struct {
	args   []string
	revoke func()
}

func (c *Client) newMCPBridgeInvocation(ctx context.Context) mcpBridgeInvocation {
	inv := mcpBridgeInvocation{revoke: func() {}}
	if c.options.MCPBridge == nil {
		return inv
	}
	sessionID, _ := ctx.Value("session_id").(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return inv
	}
	configJSON, revoke, err := c.options.MCPBridge(ctx, sessionID)
	if err != nil || strings.TrimSpace(configJSON) == "" {
		if revoke != nil {
			revoke()
		}
		if err != nil {
			logging.Warn("MCP bridge config failed, continuing without bridge: %v", err)
		}
		return inv
	}
	pluginDir, err := writeCursorMCPPlugin(configJSON)
	if err != nil {
		if revoke != nil {
			revoke()
		}
		logging.Warn("MCP bridge plugin setup failed, continuing without bridge: %v", err)
		return inv
	}
	inv.args = []string{"--plugin-dir", pluginDir, "--approve-mcps"}
	inv.revoke = func() {
		_ = os.RemoveAll(pluginDir)
		if revoke != nil {
			revoke()
		}
	}
	return inv
}

// writeCursorMCPPlugin materializes the shared bridge JSON as a Cursor plugin.
// Cursor Agent CLI has no --mcp-config flag; it loads MCP servers from plugins
// and from .cursor/mcp.json. A temp plugin keeps the bearer token out of the
// workspace and out of the user's real MCP config.
func writeCursorMCPPlugin(configJSON string) (string, error) {
	var payload struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(configJSON), &payload); err != nil {
		return "", err
	}
	if len(payload.MCPServers) == 0 {
		return "", fmt.Errorf("mcp bridge config has no servers")
	}
	servers := make(map[string]map[string]interface{}, len(payload.MCPServers))
	for name, server := range payload.MCPServers {
		if strings.TrimSpace(server.URL) == "" {
			return "", fmt.Errorf("mcp bridge server %q has no url", name)
		}
		entry := map[string]interface{}{"url": server.URL}
		if len(server.Headers) > 0 {
			entry["headers"] = server.Headers
		}
		servers[name] = entry
	}
	manifest := map[string]interface{}{
		"name":        cursorMCPPluginName,
		"description": "Session-scoped A2gent tools for this Cursor CLI run",
		"mcpServers":  servers,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "a2gent-cursor-mcp-")
	if err != nil {
		return "", err
	}
	pluginDir := filepath.Join(dir, ".cursor-plugin")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	manifestPath := filepath.Join(pluginDir, "plugin.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
