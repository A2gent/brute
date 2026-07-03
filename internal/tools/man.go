package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ManTool provides lazy, on-demand documentation for tools.
// WHY: full built-in tool descriptions in the system prompt consume a lot of
// context. WHAT: agents can query this tool when they need details for a
// specific command, similar to the Unix `man` command.
type ManTool struct {
	manager *Manager
}

type ManParams struct {
	Tool string `json:"tool,omitempty"`
	Name string `json:"name,omitempty"`
}

func NewManTool(manager *Manager) *ManTool {
	return &ManTool{manager: manager}
}

func (t *ManTool) Name() string {
	return "man"
}

func (t *ManTool) Description() string {
	return "Show the manual for an available tool. Use this when you need a tool's detailed description or input schema. If no tool is provided, lists available tool names."
}

func (t *ManTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"tool": map[string]interface{}{
				"type":        "string",
				"description": "Tool name to document. Omit to list available tool names.",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Alias for tool.",
			},
		},
	}
}

func (t *ManTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	_ = ctx
	var p ManParams
	if len(strings.TrimSpace(string(params))) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	if t.manager == nil {
		return &Result{Success: false, Error: "man tool manager is not configured"}, nil
	}

	toolName := strings.TrimSpace(p.Tool)
	if toolName == "" {
		toolName = strings.TrimSpace(p.Name)
	}
	if toolName == "" {
		return &Result{Success: true, Output: t.listTools()}, nil
	}

	toolName = normalizeToolName(toolName)
	tool, ok := t.manager.Get(toolName)
	if !ok {
		return &Result{Success: false, Error: fmt.Sprintf("tool not found: %s", toolName)}, nil
	}

	return &Result{Success: true, Output: renderToolManual(tool)}, nil
}

func (t *ManTool) listTools() string {
	defs := t.manager.GetDefinitions()
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		name := strings.TrimSpace(def.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	lines := make([]string, 0, len(names)+2)
	lines = append(lines, "Available tool manuals:")
	for _, name := range names {
		lines = append(lines, "- "+name)
	}
	lines = append(lines, "", "Call man with {\"tool\": \"<name>\"} to read details and input schema.")
	return strings.Join(lines, "\n")
}

func renderToolManual(tool Tool) string {
	schema, err := json.MarshalIndent(tool.Schema(), "", "  ")
	if err != nil {
		schema = []byte(fmt.Sprintf("<failed to encode schema: %v>", err))
	}

	parts := []string{
		"# " + tool.Name(),
		"",
		"## Description",
		strings.TrimSpace(tool.Description()),
		"",
		"## Input schema",
		"```json",
		string(schema),
		"```",
	}
	return strings.Join(parts, "\n")
}
