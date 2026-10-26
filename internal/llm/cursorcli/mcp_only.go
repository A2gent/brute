package cursorcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type mcpOnlyContextKey struct{}

type mcpOnlyWorkspace struct {
	path string
}

// WithMCPOnly marks a Cursor invocation as a child relay. Its native tools are
// disabled so all filesystem and shell work stays in the child MCP server.
func WithMCPOnly(ctx context.Context, workDir string) context.Context {
	return context.WithValue(ctx, mcpOnlyContextKey{}, mcpOnlyWorkspace{path: strings.TrimSpace(workDir)})
}

func (c *Client) forInvocation(ctx context.Context) *Client {
	workspace, ok := ctx.Value(mcpOnlyContextKey{}).(mcpOnlyWorkspace)
	if !ok {
		return c
	}
	options := c.options
	options.MCPOnly = true
	options.WorkDir = workspace.path
	options.Force = false
	options.Trust = true
	options.Sandbox = "enabled"
	return &Client{model: c.model, options: options}
}

type cursorCLIConfig struct {
	Version      int                       `json:"version"`
	ApprovalMode string                    `json:"approvalMode,omitempty"`
	Permissions  cursorCLIPermissions      `json:"permissions"`
	Sandbox      *cursorCLISandboxSettings `json:"sandbox,omitempty"`
}

type cursorCLIPermissions struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

type cursorCLISandboxSettings struct {
	Mode         string `json:"mode,omitempty"`
	ReadBoundary string `json:"readBoundary,omitempty"`
}

type cursorProjectSandboxConfig struct {
	Type            string `json:"type"`
	ReadBoundary    string `json:"readBoundary"`
	DisableTmpWrite bool   `json:"disableTmpWrite"`
}

func (c *Client) prepareMCPOnlyWorkspace() error {
	if !c.options.MCPOnly {
		return nil
	}
	if c.options.WorkDir == "" {
		return fmt.Errorf("isolated Cursor invocation is missing its temporary workspace")
	}
	cursorDir := filepath.Join(c.options.WorkDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		return fmt.Errorf("create isolated Cursor settings directory: %w", err)
	}

	permissions := cursorCLIPermissions{
		Allow: []string{"Mcp(plugin-a2gent-bridge-a2gent:*)"},
		Deny: []string{
			"Shell(*)",
			"Read(**)", "Read(/**)", "Read(~/**)",
			"Write(**)", "Write(/**)", "Write(~/**)",
			"WebFetch(*)",
		},
	}
	projectConfig := cursorCLIConfig{Version: 1, Permissions: permissions}
	if err := writePrivateJSON(filepath.Join(cursorDir, "cli.json"), projectConfig); err != nil {
		return fmt.Errorf("write isolated Cursor tool permissions: %w", err)
	}

	configDir := filepath.Join(c.options.WorkDir, ".cursor-cli-config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create isolated Cursor config directory: %w", err)
	}
	globalConfig := cursorCLIConfig{
		Version:      1,
		ApprovalMode: "allowlist",
		Permissions:  permissions,
		Sandbox:      &cursorCLISandboxSettings{Mode: "enabled", ReadBoundary: "workspace"},
	}
	if err := writePrivateJSON(filepath.Join(configDir, "cli-config.json"), globalConfig); err != nil {
		return fmt.Errorf("write isolated Cursor CLI config: %w", err)
	}

	sandboxConfig := cursorProjectSandboxConfig{
		Type:            "workspace_readwrite",
		ReadBoundary:    "workspace",
		DisableTmpWrite: true,
	}
	if err := writePrivateJSON(filepath.Join(cursorDir, "sandbox.json"), sandboxConfig); err != nil {
		return fmt.Errorf("write isolated Cursor sandbox settings: %w", err)
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
