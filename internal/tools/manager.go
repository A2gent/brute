package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	m.Register(NewReadTool(workDir))
	m.Register(NewWriteTool(workDir))
	m.Register(NewEditTool(workDir))
	m.Register(NewReplaceLinesTool(workDir))
	m.Register(NewInsertLinesTool(workDir))
	m.Register(NewGlobTool(workDir))
	m.Register(NewFindFilesTool(workDir))
	m.Register(NewGrepTool(workDir))
	m.Register(NewTakeScreenshotTool(workDir))
	m.Register(NewTakeCameraPhotoTool(workDir))

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

// Get returns a tool by name
func (m *Manager) Get(name string) (Tool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tool, ok := m.tools[name]
	return tool, ok
}

// Execute executes a tool by name with the given parameters
func (m *Manager) Execute(ctx context.Context, name string, params json.RawMessage) (*Result, error) {
	tool, ok := m.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}
	return tool.Execute(ctx, params)
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
			result, err := m.Execute(ctx, tc.Name, json.RawMessage(tc.Input))
			duration := time.Since(start)

			tr := llm.ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
			}

			if err != nil {
				tr.Content = fmt.Sprintf("Error: %v", err)
				tr.IsError = true
				logging.LogToolExecution(tc.Name, false, duration)
				logging.Debug("Tool %s error: %v", tc.Name, err)
			} else if !result.Success {
				tr.Content = fmt.Sprintf("Error: %s", result.Error)
				tr.IsError = true
				logging.LogToolExecution(tc.Name, false, duration)
				logging.Debug("Tool %s failed: %s", tc.Name, result.Error)
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
