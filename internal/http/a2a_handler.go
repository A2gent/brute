package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// handleAgentCard returns the A2A agent card for discovery.
// This endpoint is served at /.well-known/agent-card.json per A2A spec.
func (s *Server) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	port := s.port
	if port == 0 {
		port = 8080
	}

	// Get host from request or use localhost as default
	host := r.Host
	if host == "" {
		host = fmt.Sprintf("localhost:%d", port)
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	baseURL := fmt.Sprintf("%s://%s", scheme, host)

	// Get version from environment or use default
	version := os.Getenv("AAGENT_VERSION")
	if version == "" {
		version = "0.1.0"
	}

	// Get agent name from settings or use default
	agentName := "A2gent"
	if settings, err := s.store.GetSettings(); err == nil {
		if customName := strings.TrimSpace(settings[agentNameSettingKey]); customName != "" {
			agentName = customName
		}
	}

	// Get tools from tool manager
	var agentTools []AgentTool
	if s.toolManager != nil {
		toolDefs := s.toolManager.GetDefinitions()
		agentTools = make([]AgentTool, len(toolDefs))
		for i, def := range toolDefs {
			agentTools[i] = AgentTool{
				Name:        def.Name,
				Description: def.Description,
				InputSchema: def.InputSchema,
			}
		}
	}

	agentCard := AgentCard{
		Name:        agentName,
		Description: "AI agent for software engineering tasks with tool execution capabilities including file operations, shell commands, web search, browser automation, and integrations.",
		SupportedInterfaces: []AgentInterface{
			{
				URL:             baseURL,
				ProtocolBinding: "HTTP+JSON",
				ProtocolVersion: "0.1",
			},
		},
		Version:          version,
		DocumentationURL: "https://github.com/artjom/a2gent",
		Capabilities: AgentCapabilities{
			Streaming:         false,
			PushNotifications: false,
			ExtendedAgentCard: false,
		},
		DefaultInputModes:  []string{"text/plain", "application/json"},
		DefaultOutputModes: []string{"text/plain", "application/json"},
		Tools:              agentTools,
		Skills: []AgentSkill{
			{
				ID:          "code-generation",
				Name:        "Code Generation",
				Description: "Generate, modify, and refactor code across multiple programming languages with best practices.",
				Tags:        []string{"coding", "programming", "development", "refactoring"},
				Examples: []string{
					"Create a Go function to parse JSON",
					"Refactor this Python code to use async/await",
					"Add error handling to this function",
				},
				InputModes:  []string{"text/plain", "application/json"},
				OutputModes: []string{"text/plain", "application/json"},
			},
			{
				ID:          "file-operations",
				Name:        "File Operations",
				Description: "Read, write, edit, and manage files in the local filesystem.",
				Tags:        []string{"files", "filesystem", "io"},
				Examples: []string{
					"Read the contents of main.go",
					"Create a new file called config.yaml",
					"Update lines 10-20 in server.js",
				},
				InputModes:  []string{"text/plain", "application/json"},
				OutputModes: []string{"text/plain", "application/json"},
			},
			{
				ID:          "shell-execution",
				Name:        "Shell Command Execution",
				Description: "Execute shell commands safely in the project environment.",
				Tags:        []string{"shell", "bash", "commands", "terminal"},
				Examples: []string{
					"Run npm install",
					"Execute go test ./...",
					"Check git status",
				},
				InputModes:  []string{"text/plain"},
				OutputModes: []string{"text/plain"},
			},
			{
				ID:          "web-search",
				Name:        "Web Search",
				Description: "Search the web for information using Brave Search or Exa.ai.",
				Tags:        []string{"search", "web", "research", "information"},
				Examples: []string{
					"Search for Go 1.21 new features",
					"Find documentation about OAuth2 flow",
				},
				InputModes:  []string{"text/plain"},
				OutputModes: []string{"text/plain", "application/json"},
			},
			{
				ID:          "browser-automation",
				Name:        "Browser Automation",
				Description: "Control Chrome browser to navigate websites, extract content, and interact with web pages.",
				Tags:        []string{"browser", "chrome", "web", "automation", "scraping"},
				Examples: []string{
					"Navigate to example.com and get the page content",
					"Click the login button and fill credentials",
					"Take a screenshot of the dashboard",
				},
				InputModes:  []string{"text/plain", "application/json"},
				OutputModes: []string{"text/plain", "image/png"},
			},
			{
				ID:          "task-management",
				Name:        "Task Management",
				Description: "Create and manage tasks, track progress, and maintain todo lists.",
				Tags:        []string{"tasks", "todos", "planning", "organization"},
				Examples: []string{
					"Create a task to implement user authentication",
					"Mark the refactoring task as complete",
					"List all pending tasks",
				},
				InputModes:  []string{"text/plain"},
				OutputModes: []string{"text/plain", "application/json"},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(agentCard)
}
