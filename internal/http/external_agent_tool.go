package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const a2aRegistryURLSettingKey = "A2A_REGISTRY_URL"
const a2aFavoriteAgentsSettingKey = "A2A_FAVORITE_AGENTS_JSON"

type favoriteA2AAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	RegistryURL string `json:"registry_url,omitempty"`
	SavedAt     string `json:"saved_at,omitempty"`
}

type delegateToExternalAgentTool struct {
	server *Server
}

type delegateToExternalAgentParams struct {
	TargetAgentID   string `json:"target_agent_id,omitempty"`
	TargetAgentName string `json:"target_agent_name,omitempty"`
	Task            string `json:"task"`
}

type discoverExternalAgentsTool struct {
	server *Server
}

type discoverExternalAgentsParams struct {
	Query    string `json:"query,omitempty"`
	Type     string `json:"type,omitempty"`
	Status   string `json:"status,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"page_size,omitempty"`
}

type registryDiscoveredAgent struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     string  `json:"description,omitempty"`
	Status          string  `json:"status,omitempty"`
	AgentType       string  `json:"agent_type,omitempty"`
	Discoverable    bool    `json:"discoverable"`
	PricePerRequest float64 `json:"price_per_request,omitempty"`
	Currency        string  `json:"currency,omitempty"`
	CreatedAt       string  `json:"created_at,omitempty"`
}

type registryDiscoveryResponse struct {
	Agents   []registryDiscoveredAgent `json:"agents"`
	Total    int                       `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"page_size"`
}

func newDelegateToExternalAgentTool(server *Server) *delegateToExternalAgentTool {
	return &delegateToExternalAgentTool{server: server}
}

func (t *delegateToExternalAgentTool) Name() string {
	return "delegate_to_external_agent"
}

func (t *delegateToExternalAgentTool) Description() string {
	return `Delegate a task to an external A2A registry agent. Prefer target_agent_id from your favorites list for deterministic routing.

If target_agent_id is omitted, target_agent_name is matched against favorites. Returns the external agent response and child session ID.`
}

func (t *delegateToExternalAgentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target_agent_id": map[string]interface{}{
				"type":        "string",
				"description": "Target external agent ID from favorites or registry.",
			},
			"target_agent_name": map[string]interface{}{
				"type":        "string",
				"description": "Optional favorite name alias when target_agent_id is not provided.",
			},
			"task": map[string]interface{}{
				"type":        "string",
				"description": "Task to delegate to the selected external agent.",
			},
		},
		"required": []string{"task"},
	}
}

func (t *delegateToExternalAgentTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p delegateToExternalAgentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	task := strings.TrimSpace(p.Task)
	if task == "" {
		return &tools.Result{Success: false, Error: "task is required"}, nil
	}

	settings, err := t.server.store.GetSettings()
	if err != nil {
		settings = map[string]string{}
	}
	favorites := parseFavoriteA2AAgents(settings[a2aFavoriteAgentsSettingKey])

	targetAgentID := strings.TrimSpace(p.TargetAgentID)
	targetAgentName := strings.TrimSpace(p.TargetAgentName)
	if targetAgentID == "" {
		favorite, resolveErr := resolveFavoriteExternalAgent(favorites, targetAgentName)
		if resolveErr != nil {
			return &tools.Result{Success: false, Error: resolveErr.Error()}, nil
		}
		targetAgentID = favorite.ID
		if targetAgentName == "" {
			targetAgentName = favorite.Name
		}
	}

	parentSessionID, _ := ctx.Value("session_id").(string)
	childSess, err := t.server.sessionManager.Create("a2a")
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to create child session: " + err.Error()}, nil
	}
	if parentSessionID != "" {
		childSess.ParentID = &parentSessionID
		if parentSess, parentErr := t.server.sessionManager.Get(parentSessionID); parentErr == nil && parentSess.ProjectID != nil {
			childSess.ProjectID = parentSess.ProjectID
		}
	}
	if childSess.Metadata == nil {
		childSess.Metadata = map[string]interface{}{}
	}
	childSess.Metadata[a2aOutboundSessionKey] = true
	childSess.Metadata[a2aOutboundTargetAgentIDKey] = targetAgentID
	childSess.Metadata[a2aOutboundTargetAgentNameKey] = targetAgentName
	childSess.Metadata[a2aConversationIDKey] = childSess.ID
	childSess.SetTitle(targetAgentName)
	childSess.SetStatus(session.StatusPaused)
	if err := t.server.sessionManager.Save(childSess); err != nil {
		return &tools.Result{Success: false, Error: "failed to persist child session: " + err.Error()}, nil
	}

	body, err := json.Marshal(ChatRequest{Message: task})
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	chatResp, chatErr := t.server.processA2AOutboundChat(ctx, childSess.ID, body)
	if chatErr != nil {
		metadata := map[string]interface{}{
			"child_session_id":    childSess.ID,
			"external_agent_id":   targetAgentID,
			"external_agent_name": targetAgentName,
		}
		return &tools.Result{Success: false, Error: chatErr.message, Metadata: metadata}, nil
	}

	responseText := strings.TrimSpace(chatResp.Content)
	if len(responseText) > 4000 {
		responseText = responseText[:4000] + "\n...(truncated)"
	}

	payload := map[string]interface{}{
		"success":             true,
		"child_session_id":    childSess.ID,
		"external_agent_id":   targetAgentID,
		"external_agent_name": targetAgentName,
		"response":            responseText,
		"status":              chatResp.Status,
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool output: %w", err)
	}

	return &tools.Result{
		Success: true,
		Output:  string(encoded),
		Metadata: map[string]interface{}{
			"child_session_id":    childSess.ID,
			"external_agent_id":   targetAgentID,
			"external_agent_name": targetAgentName,
		},
	}, nil
}

func newDiscoverExternalAgentsTool(server *Server) *discoverExternalAgentsTool {
	return &discoverExternalAgentsTool{server: server}
}

func (t *discoverExternalAgentsTool) Name() string {
	return "discover_external_agents"
}

func (t *discoverExternalAgentsTool) Description() string {
	return "Search external agents on the configured A2 registry and return a compact list with IDs, names, status, and type."
}

func (t *discoverExternalAgentsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Optional name query.",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Optional agent type filter (personal|business|government).",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"description": "Optional status filter (active|inactive|suspended).",
			},
			"page": map[string]interface{}{
				"type":        "integer",
				"description": "Page number (default 1).",
			},
			"page_size": map[string]interface{}{
				"type":        "integer",
				"description": "Results per page (default 20, max 100).",
			},
		},
	}
}

func (t *discoverExternalAgentsTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p discoverExternalAgentsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	settings, err := t.server.store.GetSettings()
	if err != nil {
		settings = map[string]string{}
	}
	registryURL, apiKey, err := t.server.resolveA2ARegistryConnection(settings)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	page := p.Page
	if page <= 0 {
		page = 1
	}
	pageSize := p.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	queryParams := url.Values{}
	if q := strings.TrimSpace(p.Query); q != "" {
		queryParams.Set("name", q)
	}
	if v := strings.TrimSpace(p.Type); v != "" {
		queryParams.Set("agent_type", v)
	}
	if v := strings.TrimSpace(p.Status); v != "" {
		queryParams.Set("status", v)
	}
	queryParams.Set("page", strconv.Itoa(page))
	queryParams.Set("page_size", strconv.Itoa(pageSize))

	reqURL := strings.TrimRight(registryURL, "/") + "/agents/discover"
	if encoded := queryParams.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: "registry discovery request failed: " + err.Error()}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return &tools.Result{Success: false, Error: "registry discovery failed: " + msg}, nil
	}

	var discovered registryDiscoveryResponse
	if err := json.Unmarshal(body, &discovered); err != nil {
		return &tools.Result{Success: false, Error: "failed to decode registry response: " + err.Error()}, nil
	}

	favoriteSet := map[string]struct{}{}
	for _, fav := range parseFavoriteA2AAgents(settings[a2aFavoriteAgentsSettingKey]) {
		favoriteSet[fav.ID] = struct{}{}
	}

	items := make([]map[string]interface{}, 0, len(discovered.Agents))
	for _, ag := range discovered.Agents {
		_, isFavorite := favoriteSet[strings.TrimSpace(ag.ID)]
		items = append(items, map[string]interface{}{
			"id":           strings.TrimSpace(ag.ID),
			"name":         strings.TrimSpace(ag.Name),
			"status":       strings.TrimSpace(ag.Status),
			"agent_type":   strings.TrimSpace(ag.AgentType),
			"description":  strings.TrimSpace(ag.Description),
			"discoverable": ag.Discoverable,
			"price":        ag.PricePerRequest,
			"currency":     strings.TrimSpace(ag.Currency),
			"created_at":   strings.TrimSpace(ag.CreatedAt),
			"is_favorited": isFavorite,
		})
	}

	payload := map[string]interface{}{
		"registry_url": registryURL,
		"total":        discovered.Total,
		"page":         discovered.Page,
		"page_size":    discovered.PageSize,
		"agents":       items,
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool output: %w", err)
	}
	return &tools.Result{Success: true, Output: string(encoded)}, nil
}

func (s *Server) resolveA2ARegistryConnection(settings map[string]string) (registryURL string, apiKey string, err error) {
	registryURL = strings.TrimSpace(settings[a2aRegistryURLSettingKey])

	integrations, listErr := s.store.ListIntegrations()
	if listErr != nil {
		return "", "", fmt.Errorf("failed to list integrations: %w", listErr)
	}
	var integration *storage.Integration
	for _, item := range integrations {
		if item == nil || item.Provider != "a2_registry" {
			continue
		}
		integration = item
		break
	}
	if integration == nil {
		return "", "", fmt.Errorf("a2_registry integration is not configured")
	}
	apiKey = strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		return "", "", fmt.Errorf("a2_registry integration api_key is missing")
	}
	if registryURL == "" {
		registryURL = strings.TrimSpace(integration.Config["registry_url"])
	}
	if registryURL == "" {
		return "", "", fmt.Errorf("registry URL is not configured; set it in A2 Registry settings")
	}
	registryURL = strings.TrimRight(registryURL, "/")
	return registryURL, apiKey, nil
}

func resolveFavoriteExternalAgent(favorites []favoriteA2AAgent, requestedName string) (*favoriteA2AAgent, error) {
	if len(favorites) == 0 {
		return nil, fmt.Errorf("no favorite external agents configured; add favorites in External agents")
	}
	name := strings.ToLower(strings.TrimSpace(requestedName))
	if name == "" {
		return nil, fmt.Errorf("target_agent_id or target_agent_name is required")
	}
	for i := range favorites {
		if strings.ToLower(strings.TrimSpace(favorites[i].Name)) == name {
			return &favorites[i], nil
		}
	}

	matches := make([]*favoriteA2AAgent, 0, 4)
	for i := range favorites {
		if strings.Contains(strings.ToLower(strings.TrimSpace(favorites[i].Name)), name) {
			matches = append(matches, &favorites[i])
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, item := range matches {
			names = append(names, strings.TrimSpace(item.Name))
		}
		sort.Strings(names)
		return nil, fmt.Errorf("target_agent_name is ambiguous; matches: %s", strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("favorite external agent not found for target_agent_name=%q", requestedName)
}

func parseFavoriteA2AAgents(raw string) []favoriteA2AAgent {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var parsed []favoriteA2AAgent
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		logging.Warn("Failed to parse %s: %v", a2aFavoriteAgentsSettingKey, err)
		return nil
	}
	cleaned := make([]favoriteA2AAgent, 0, len(parsed))
	for _, item := range parsed {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		cleaned = append(cleaned, favoriteA2AAgent{
			ID:          id,
			Name:        strings.TrimSpace(item.Name),
			Description: strings.TrimSpace(item.Description),
			RegistryURL: strings.TrimSpace(item.RegistryURL),
			SavedAt:     strings.TrimSpace(item.SavedAt),
		})
	}
	return cleaned
}

func (s *Server) resolveExternalAgentsSection(settings map[string]string) (string, int) {
	favorites := parseFavoriteA2AAgents(settings[a2aFavoriteAgentsSettingKey])
	if len(favorites) == 0 {
		return "", 0
	}

	lines := make([]string, 0, len(favorites)+4)
	lines = append(lines, "Available external agents for delegation:")
	lines = append(lines, "Use delegate_to_external_agent to offload tasks to favorited registry agents.")
	lines = append(lines, "Use discover_external_agents to search the registry when a suitable favorite is missing.")
	lines = append(lines, "")
	for _, fav := range favorites {
		name := strings.TrimSpace(fav.Name)
		if name == "" {
			name = "(unnamed)"
		}
		lines = append(lines, fmt.Sprintf("- ID: %s | Name: %s", fav.ID, name))
	}
	section := strings.Join(lines, "\n")
	return section, estimateTokensApprox(section)
}

var _ tools.Tool = (*delegateToExternalAgentTool)(nil)
var _ tools.Tool = (*discoverExternalAgentsTool)(nil)
