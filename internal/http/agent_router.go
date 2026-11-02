package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

type routableAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type agentRouterResponse struct {
	RouterProvider        string          `json:"router_provider"`
	RouterModel           string          `json:"router_model"`
	RouterReasoningEffort string          `json:"router_reasoning_effort"`
	Agents                []routableAgent `json:"agents"`
}

type agentRouterRequest struct {
	RouterProvider        string                    `json:"router_provider"`
	RouterModel           string                    `json:"router_model"`
	RouterReasoningEffort string                    `json:"router_reasoning_effort"`
	Agents                []config.AgentRouterAgent `json:"agents"`
}

func agentDescription(def *agentdef.Definition) string {
	if def == nil {
		return ""
	}
	parts := []string{}
	if d := strings.TrimSpace(def.Agent.Description); d != "" {
		parts = append(parts, d)
	}
	if k := strings.TrimSpace(def.Agent.Kind); k != "" {
		parts = append(parts, "kind: "+k)
	}
	return strings.Join(parts, "; ")
}

func (s *Server) routableAgents(projectID string, allProjects bool) []routableAgent {
	projectID = strings.TrimSpace(projectID)
	seen := map[string]bool{}
	result := []routableAgent{}
	add := func(id, name, description, agentProjectID string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		if !allProjects && !agentVisibleInProjectSession(agentProjectID, projectID) {
			return
		}
		seen[id] = true
		if strings.TrimSpace(name) == "" {
			name = id
		}
		result = append(result, routableAgent{
			ID:          id,
			Name:        strings.TrimSpace(name),
			Description: description,
			ProjectID:   strings.TrimSpace(agentProjectID),
			Enabled:     s.config.AgentRouter.AgentEnabled(id),
		})
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		settings = map[string]string{}
	}
	globalCatalogDir := s.resolveGlobalAgentDefinitionsDirectory(settings)

	if records, err := s.store.ListAgentDefinitions(); err == nil {
		for _, record := range records {
			if record == nil {
				continue
			}
			agentProjectID := agentDefinitionRecordProjectID(record)
			def, parseErr := agentdef.ParseYAML([]byte(record.DefinitionYAML))
			if parseErr != nil {
				continue
			}
			if agentProjectID == "" {
				agentProjectID = stringFromOptional(projectIDFromDefinition(def))
			}
			if d := strings.TrimSpace(def.Local.DefinitionDir); d != "" && pathWithinDirectory(d, globalCatalogDir) {
				agentProjectID = ""
			}
			add(record.ID, record.Name, agentDescription(def), agentProjectID)
		}
	}

	if subAgents, err := s.store.ListSubAgents(); err == nil {
		for _, sa := range subAgents {
			add(sa.ID, sa.Name, "", subAgentProjectID(sa))
		}
	}

	addDiscovered := func(dir string, scopeProjectID string) {
		items, _ := discoverAgentDefinitionsInDirectory(dir)
		for _, item := range items {
			agentProjectID := strings.TrimSpace(item.ProjectID)
			if agentProjectID == "" {
				agentProjectID = scopeProjectID
			}
			add(item.ID, item.Name, agentDescription(item.Definition), agentProjectID)
		}
	}
	addDiscovered(globalCatalogDir, "")

	projects := []*storage.Project{}
	if allProjects {
		if list, err := s.store.ListProjects(); err == nil {
			projects = list
		}
	} else if projectID != "" {
		if project, err := s.store.GetProject(projectID); err == nil && project != nil {
			projects = append(projects, project)
		}
	}
	for _, project := range projects {
		if project == nil || project.ID == storage.SystemProjectSoulID {
			continue
		}
		addDiscovered(s.resolveScopedProjectAgentDefinitionsDirectory(project), project.ID)
	}
	return result
}

func (s *Server) agentRouterResponse() agentRouterResponse {
	cfg := s.config.AgentRouter
	return agentRouterResponse{
		RouterProvider:        config.NormalizeProviderRef(cfg.RouterProvider),
		RouterModel:           strings.TrimSpace(cfg.RouterModel),
		RouterReasoningEffort: strings.TrimSpace(cfg.RouterReasoningEffort),
		Agents:                s.routableAgents("", true),
	}
}

func (s *Server) handleGetAgentRouter(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.agentRouterResponse())
}

func (s *Server) validateAgentRouterProvider(providerRef string, model string) error {
	if providerRef == "" {
		if model != "" {
			return fmt.Errorf("router_model requires router_provider")
		}
		return nil
	}
	ptype := config.ProviderType(providerRef)
	if ptype == config.ProviderAutoRouter {
		return fmt.Errorf("agent router cannot use automatic_router as router provider")
	}
	if config.IsFallbackAggregateRef(providerRef) || ptype == config.ProviderFallback {
		if model != "" {
			return fmt.Errorf("router_model is not allowed when router_provider is a fallback chain")
		}
		_, err := s.fallbackNodesForProvider(ptype)
		return err
	}
	if config.GetProviderDefinitionForRef(providerRef) == nil {
		return fmt.Errorf("router_provider is unsupported: %s", providerRef)
	}
	if !s.providerConfiguredForUse(ptype) {
		return fmt.Errorf("router_provider %s is not configured", providerRef)
	}
	return nil
}

func (s *Server) handleUpdateAgentRouter(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != "" {
		s.errorResponse(w, http.StatusForbidden, "Agent router settings are managed by the parent agent in Docker safe mode")
		return
	}
	var req agentRouterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	next := config.AgentRouter{
		RouterProvider:        config.NormalizeProviderRef(req.RouterProvider),
		RouterModel:           strings.TrimSpace(req.RouterModel),
		RouterReasoningEffort: strings.TrimSpace(req.RouterReasoningEffort),
	}
	if err := s.validateAgentRouterProvider(next.RouterProvider, next.RouterModel); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	seen := map[string]bool{}
	for _, agent := range req.Agents {
		id := strings.TrimSpace(agent.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		next.Agents = append(next.Agents, config.AgentRouterAgent{ID: id, Enabled: agent.Enabled})
	}
	previous := s.config.AgentRouter
	s.config.AgentRouter = next
	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.config.AgentRouter = previous
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save agent router: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.agentRouterResponse())
}

func enabledRoutableAgents(agents []routableAgent) []routableAgent {
	enabled := make([]routableAgent, 0, len(agents))
	for _, agent := range agents {
		if agent.Enabled {
			enabled = append(enabled, agent)
		}
	}
	return enabled
}

func (s *Server) routeSessionAgent(ctx context.Context, projectID string, prompt string) (string, string, error) {
	candidates := enabledRoutableAgents(s.routableAgents(projectID, false))
	if len(candidates) == 0 {
		return "", "", nil
	}
	if len(candidates) == 1 {
		return candidates[0].ID, "single enabled agent", nil
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", "", fmt.Errorf("cannot route an empty prompt")
	}
	providerRef, model, effort := s.config.EffectiveAgentRouterModel()
	if providerRef == "" {
		return "", "", fmt.Errorf("agent router has no router_provider configured")
	}
	ptype := config.ProviderType(providerRef)
	if config.IsFallbackAggregateRef(providerRef) || ptype == config.ProviderFallback {
		model = ""
	}
	client, err := s.createLLMClient(ptype, model, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to initialize agent router provider: %w", err)
	}
	if effort == "" {
		resolved := model
		if resolved == "" {
			resolved = s.resolveModelForProvider(ptype)
		}
		effort = auxiliaryReasoningEffort(ptype, resolved)
	}
	return selectAgentViaLLM(ctx, client, model, effort, prompt, candidates)
}

func selectAgentViaLLM(ctx context.Context, client llm.Client, model string, effort string, prompt string, candidates []routableAgent) (string, string, error) {
	type indexedAgent struct {
		Index       int    `json:"index"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	indexed := make([]indexedAgent, 0, len(candidates))
	for i, agent := range candidates {
		indexed = append(indexed, indexedAgent{Index: i + 1, Name: agent.Name, Description: agent.Description})
	}
	agentsJSON, _ := json.Marshal(indexed)
	req := &llm.ChatRequest{
		SystemPrompt:    agentRouterSystemPrompt,
		ReasoningEffort: effort,
		Messages: []llm.Message{
			{Role: "user", Content: fmt.Sprintf("Agents: %s\n\nUser prompt: %s", string(agentsJSON), llm.HeadTail(prompt, llm.RouterPromptMaxBytes))},
		},
		Temperature: 0,
		MaxTokens:   4096,
		Model:       model,
	}
	resp, err := client.Chat(ctx, req)
	if err != nil {
		return "", "", err
	}
	choice, err := parseRouterChoice(resp.Content)
	if err != nil {
		return "", "", err
	}
	if choice.Index < 1 || choice.Index > len(candidates) {
		return "", "", fmt.Errorf("agent router returned out-of-range index %d; expected 1-%d", choice.Index, len(candidates))
	}
	selected := candidates[choice.Index-1]
	logging.Info("Agent router selected agent=%s reason=%s", selected.ID, choice.Reason)
	return selected.ID, strings.TrimSpace(choice.Reason), nil
}

const agentRouterSystemPrompt = `You are a strict agent router. Choose the single agent whose name and description best fit the primary action the user is asking for.

- Classify by the requested deliverable and action, not by incidental words.
- Choose only a 1-based index that exists in the supplied Agents array. Never return 0, -1, or an invented agent.

Return exactly one complete JSON object on one line and nothing else:
{"index":<1-based integer>,"reason":"brief explanation"}`

func (s *Server) applyAgentAutoRoute(ctx context.Context, req *CreateSessionRequest, sess *session.Session) {
	if !req.AgentAutoRoute || strings.TrimSpace(req.UnifiedAgentID+req.DockerAgentID+req.SubAgentID) != "" {
		return
	}
	agentID, reason, err := s.routeSessionAgent(ctx, req.ProjectID, req.Task)
	if err != nil {
		logging.Warn("Agent router failed, using default session agent: %v", err)
		return
	}
	if agentID == "" {
		return
	}
	req.UnifiedAgentID = agentID
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata["agent_routed"] = true
	sess.Metadata["agent_route_reason"] = reason
}
