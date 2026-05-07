package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
)

// Tool defines the interface for executable tools
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]interface{}
	Execute(ctx context.Context, params json.RawMessage) (*Result, error)
}

// Result represents a tool execution result
type Result struct {
	Success  bool                   `json:"success"`
	Output   string                 `json:"output"`
	Error    string                 `json:"error,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Manager manages available tools
type Manager struct {
	tools   map[string]Tool
	workDir string
	mu      sync.RWMutex
}

// Clone creates a shallow copy of the manager preserving tool registrations.
func (m *Manager) Clone() *Manager {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	cloned := &Manager{
		tools:   make(map[string]Tool, len(m.tools)),
		workDir: m.workDir,
	}
	for name, tool := range m.tools {
		cloned.tools[name] = tool
	}
	return cloned
}

// WorkDir returns manager work directory.
func (m *Manager) WorkDir() string {
	if m == nil {
		return ""
	}
	return m.workDir
}

// NewManager creates a new tool manager
func NewManager(workDir string) *Manager {
	m := &Manager{
		tools:   make(map[string]Tool),
		workDir: workDir,
	}

	// Register built-in tools
	m.Register(NewBashTool(workDir))
	m.Register(NewCodeExecutionTool(workDir))
	m.Register(NewReadTool(workDir))
	m.Register(NewWriteTool(workDir))
	m.Register(NewEditTool(workDir))
	m.Register(NewReplaceLinesTool(workDir))
	m.Register(NewInsertLinesTool(workDir))
	m.Register(NewGlobTool(workDir))
	m.Register(NewFindFilesTool(workDir))
	m.Register(NewGrepTool(workDir))
	m.Register(NewFilterTool(workDir))
	m.Register(NewRandomNumberTool())
	m.Register(NewCurrentTimeTool())
	m.Register(NewTakeScreenshotTool(workDir))
	m.Register(NewTakeCameraPhotoTool(workDir))
	m.Register(NewPipelineTool(m))
	m.Register(NewParallelTool(m))

	return m
}

// NewManagerWithStore creates a tool manager and registers store-backed tools.
func NewManagerWithStore(workDir string, store storage.Store) *Manager {
	_ = store
	return NewManager(workDir)
}

// RegisterQuestionTool registers the question tool with a session metadata store
func (m *Manager) RegisterQuestionTool(store QuestionSessionStore) {
	m.Register(NewQuestionTool(store))
}

// RegisterSessionTaskProgressTool registers the session task progress tool
func (m *Manager) RegisterSessionTaskProgressTool(store TaskProgressStore) {
	m.Register(NewSessionTaskProgressTool(store))
}

// Register adds a tool to the manager
func (m *Manager) Register(tool Tool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools[tool.Name()] = tool
}

// Unregister removes a tool by name.
func (m *Manager) Unregister(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tools, name)
}

// Get returns a tool by name
func (m *Manager) Get(name string) (Tool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tool, ok := m.tools[normalizeToolName(name)]
	return tool, ok
}

// Execute executes a tool by name with the given parameters
func (m *Manager) Execute(ctx context.Context, name string, params json.RawMessage) (*Result, error) {
	toolName := normalizeToolName(name)
	tool, ok := m.Get(toolName)
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", toolName)
	}
	return tool.Execute(ctx, params)
}

func normalizeToolName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	if idx := strings.LastIndex(trimmed, "."); idx >= 0 && idx+1 < len(trimmed) {
		return strings.TrimSpace(trimmed[idx+1:])
	}
	return trimmed
}

// ExecuteParallel executes multiple tool calls in parallel
func (m *Manager) ExecuteParallel(ctx context.Context, calls []llm.ToolCall) []llm.ToolResult {
	results := make([]llm.ToolResult, len(calls))
	var wg sync.WaitGroup

	logging.Debug("Executing %d tool(s) in parallel", len(calls))

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, tc llm.ToolCall) {
			defer wg.Done()

			start := time.Now()
			callCtx := context.WithValue(ctx, "tool_call_id", tc.ID)
			toolName := normalizeToolName(tc.Name)
			result, err := m.Execute(callCtx, toolName, json.RawMessage(tc.Input))
			duration := time.Since(start)

			tr := llm.ToolResult{
				ToolCallID: tc.ID,
				Name:       toolName,
				DurationMs: duration.Milliseconds(),
			}

			if err != nil {
				tr.Content = fmt.Sprintf("Error: %v", err)
				tr.IsError = true
				logging.LogToolExecution(tc.Name, false, duration)
				logging.Debug("Tool %s error: %v", tc.Name, err)
			} else if !result.Success {
				message := strings.TrimSpace(result.Error)
				if message == "" {
					message = strings.TrimSpace(result.Output)
				}
				if message == "" {
					message = "tool returned unsuccessful result"
				}
				tr.Content = fmt.Sprintf("Error: %s", message)
				tr.IsError = true
				logging.LogToolExecution(toolName, false, duration)
				logging.Debug("Tool %s failed: %s", toolName, message)
			} else {
				tr.Content = result.Output
				tr.Metadata = result.Metadata
				logging.LogToolExecution(tc.Name, true, duration)
			}

			results[idx] = tr
		}(i, call)
	}

	wg.Wait()
	return results
}

// GetDefinitions returns tool definitions for LLM
func (m *Manager) GetDefinitions() []llm.ToolDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	defs := make([]llm.ToolDefinition, 0, len(m.tools))
	for _, tool := range m.tools {
		defs = append(defs, llm.ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: tool.Schema(),
		})
	}
	return defs
}
