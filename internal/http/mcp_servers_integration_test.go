//go:build integration

package http

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestMCPStdioFetchServer(t *testing.T) {
	if _, err := exec.LookPath("uvx"); err != nil {
		t.Skip("uvx not found in PATH")
	}

	s := &Server{}
	cfg := &mcpServerConfig{
		Name:      "mcp-server-fetch",
		Transport: mcpTransportStdio,
		Enabled:   true,
		Command:   "uvx",
		// Pin the compatible MCP v1 line because mcp-server-fetch currently allows
		// the API-incompatible mcp v2 release.
		Args:           []string{"--with", "mcp==1.29.0", "mcp-server-fetch==2026.7.10"},
		TimeoutSeconds: 120,
	}

	result := s.testMCPServer(context.Background(), cfg)
	if result == nil {
		t.Fatalf("expected test result, got nil")
	}
	if !result.Success {
		t.Fatalf("mcp test failed: %s\nlogs:\n%s", result.Message, strings.Join(result.Logs, "\n"))
	}
	if result.ToolCount <= 0 {
		t.Fatalf("expected tools to be exposed, got %d\nlogs:\n%s", result.ToolCount, strings.Join(result.Logs, "\n"))
	}
}
