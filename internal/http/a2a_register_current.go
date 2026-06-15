package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/google/uuid"
)

const (
	a2aRegistryOwnerEmailSettingKey  = "A2A_REGISTRY_OWNER_EMAIL"
	a2aRegistryAgentHandleSettingKey = "A2A_REGISTRY_AGENT_HANDLE"
)

type registerCurrentA2AAgentRequest struct {
	RegistryURL        string  `json:"registry_url"`
	OwnerEmail         string  `json:"owner_email"`
	AgentName          string  `json:"agent_name"`
	AgentHandle        string  `json:"agent_handle"`
	Description        string  `json:"description"`
	AgentType          string  `json:"agent_type"`
	Category           string  `json:"category"`
	Discoverable       *bool   `json:"discoverable"`
	SupportsAudio      *bool   `json:"supports_audio"`
	SupportsImages     *bool   `json:"supports_images"`
	SupportsVideo      *bool   `json:"supports_video"`
	PricingModel       string  `json:"pricing_model"`
	PricePerRequest    float64 `json:"price_per_request"`
	PricePerInputKB    float64 `json:"price_per_input_kb"`
	PricePerOutputKB   float64 `json:"price_per_output_kb"`
	PricePerSession    float64 `json:"price_per_session"`
	Currency           string  `json:"currency"`
	Transport          string  `json:"transport"`
	SquareGRPCAddr     string  `json:"square_grpc_addr"`
	SquareWSURL        string  `json:"square_ws_url"`
	OfficialWebsite    string  `json:"official_website"`
	AvatarURL          string  `json:"avatar_url"`
	OrganizationHandle string  `json:"organization_handle"`
}

type registerCurrentA2AAgentResponse struct {
	RegistryAgentID       string `json:"registry_agent_id"`
	RegistryAgentName     string `json:"registry_agent_name"`
	RegistryAgentHandle   string `json:"registry_agent_handle,omitempty"`
	RegistryAgentPublicID string `json:"registry_agent_public_id,omitempty"`
	RegistryAPIKey        string `json:"registry_api_key"`
	RegistryURL           string `json:"registry_url"`
	OwnerEmail            string `json:"owner_email"`
	IntegrationID         string `json:"integration_id"`
	TunnelState           string `json:"tunnel_state,omitempty"`
	TunnelNote            string `json:"tunnel_note,omitempty"`
	ApprovalStatus        string `json:"approval_status,omitempty"`
	Message               string `json:"message,omitempty"`
}

func (s *Server) handleRegisterCurrentA2AAgent(w http.ResponseWriter, r *http.Request) {
	var req registerCurrentA2AAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	resp, statusCode, err := s.registerCurrentA2AAgent(r.Context(), req)
	if err != nil {
		s.errorResponse(w, statusCode, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) registerCurrentA2AAgent(ctx context.Context, req registerCurrentA2AAgentRequest) (*registerCurrentA2AAgentResponse, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.store == nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("settings store is not configured")
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to load settings: %w", err)
	}
	if settings == nil {
		settings = map[string]string{}
	}
	defaults := s.localDockerAgentRegistryDefaults()

	registryURL := strings.TrimRight(strings.TrimSpace(req.RegistryURL), "/")
	if registryURL == "" {
		registryURL = strings.TrimRight(strings.TrimSpace(defaults.RegistryURL), "/")
	}
	if registryURL == "" {
		registryURL = defaultLocalRegistryURL
	}

	ownerEmail := strings.TrimSpace(req.OwnerEmail)
	if ownerEmail == "" {
		ownerEmail = strings.TrimSpace(settings[a2aRegistryOwnerEmailSettingKey])
	}
	if ownerEmail == "" {
		ownerEmail = strings.TrimSpace(defaults.OwnerEmail)
	}
	if ownerEmail == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("owner_email is required; set it in A2 Registry settings before registering this agent")
	}

	agentName := strings.TrimSpace(req.AgentName)
	if agentName == "" {
		agentName = strings.TrimSpace(settings[agentNameSettingKey])
	}
	if agentName == "" {
		agentName = defaultAgentName
	}

	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = "Local A2gent agent connected through the A2A tunnel."
	}

	organizationHandle := slugifyForA2AgentHandle(req.OrganizationHandle)
	agentHandle := firstNonEmptyLocalAgentString(req.AgentHandle, settings[a2aRegistryAgentHandleSettingKey], defaultsAgentHandle(agentName))
	if strings.Contains(agentHandle, "/") {
		parts := strings.SplitN(agentHandle, "/", 2)
		if organizationHandle == "" {
			organizationHandle = slugifyForA2AgentHandle(parts[0])
		}
		agentHandle = strings.TrimSpace(parts[1])
	}
	agentHandle = slugifyForA2AgentHandle(agentHandle)
	if len(agentHandle) > 64 {
		agentHandle = strings.Trim(agentHandle[:64], "-")
	}
	if agentHandle == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("agent_handle is required")
	}

	agentType := strings.TrimSpace(req.AgentType)
	if agentType == "" {
		agentType = "personal"
	}
	if organizationHandle != "" && agentType == "personal" {
		// WHY: Square treats organization-scoped handles as business agents.
		agentType = "business"
	}
	category := strings.TrimSpace(req.Category)
	if category == "" {
		category = "personal"
	}
	discoverable := true
	if req.Discoverable != nil {
		discoverable = *req.Discoverable
	}
	supportsImages := true
	if req.SupportsImages != nil {
		supportsImages = *req.SupportsImages
	}
	supportsAudio := true
	if req.SupportsAudio != nil {
		supportsAudio = *req.SupportsAudio
	}
	supportsVideo := false
	if req.SupportsVideo != nil {
		supportsVideo = *req.SupportsVideo
	}
	pricePerSession := req.PricePerSession
	if req.PricePerRequest <= 0 && req.PricePerInputKB <= 0 && req.PricePerOutputKB <= 0 && pricePerSession <= 0 {
		pricePerSession = 0.001
	}
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "USD"
	}

	registerReq := squareRegisterAgentRequest{
		Name:               agentName,
		AgentHandle:        agentHandle,
		OrganizationHandle: organizationHandle,
		Description:        description,
		NetworkAccess:      "behind_nat",
		AgentType:          agentType,
		Category:           category,
		OwnerEmail:         ownerEmail,
		PricingModel:       strings.TrimSpace(req.PricingModel),
		PricePerRequest:    req.PricePerRequest,
		PricePerInputKB:    req.PricePerInputKB,
		PricePerOutputKB:   req.PricePerOutputKB,
		PricePerSession:    pricePerSession,
		Currency:           currency,
		Discoverable:       &discoverable,
		OfficialWebsite:    strings.TrimSpace(req.OfficialWebsite),
		AvatarURL:          strings.TrimSpace(req.AvatarURL),
		SupportsImages:     &supportsImages,
		SupportsAudio:      &supportsAudio,
		SupportsVideo:      &supportsVideo,
	}

	registerResp, rawMessage, statusCode, err := postSquareAgentRegistration(ctx, registryURL, registerReq, "")
	if err != nil {
		return nil, statusCode, err
	}

	transport := strings.TrimSpace(strings.ToLower(req.Transport))
	if transport == "" {
		transport = strings.TrimSpace(strings.ToLower(defaults.Transport))
	}
	if transport != "grpc" && transport != "websocket" {
		transport = "grpc"
	}
	squareGRPCAddr := strings.TrimSpace(req.SquareGRPCAddr)
	if squareGRPCAddr == "" {
		squareGRPCAddr = strings.TrimSpace(defaults.SquareGRPCAddr)
	}
	if squareGRPCAddr == "" {
		squareGRPCAddr = defaultSquareGRPCAddr
	}
	squareWSURL := strings.TrimSpace(req.SquareWSURL)
	if squareWSURL == "" {
		squareWSURL = strings.TrimSpace(defaults.SquareWSURL)
	}
	if transport == "websocket" && squareWSURL == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("square_ws_url is required when transport is websocket")
	}

	config := map[string]string{
		"api_key":          registerResp.APIKey,
		"registry_url":     registryURL,
		"owner_email":      ownerEmail,
		"transport":        transport,
		"square_grpc_addr": squareGRPCAddr,
		"square_ws_url":    squareWSURL,
		"agent_id":         registerResp.Agent.ID,
		"agent_name":       registerResp.Agent.Name,
		"agent_handle":     registerResp.Agent.AgentHandle,
		"agent_public_id":  registerResp.Agent.PublicID,
	}
	integrationID, err := s.upsertA2ARegistryIntegration(config, true)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to save A2 Registry integration: %w", err)
	}

	settings[a2aRegistryURLSettingKey] = registryURL
	settings[a2aRegistryOwnerEmailSettingKey] = ownerEmail
	settings[a2aRegistryAgentHandleSettingKey] = agentHandle
	if err := s.store.SaveSettings(settings); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to save A2 Registry settings: %w", err)
	}

	// WHY: the saved integration contains a freshly issued token, so reconnect now
	// and let Square keep rejecting it until the owner approves this pending agent.
	s.runA2ATunnelIfConfigured()

	tunnelState := ""
	if s.tunnelClient != nil {
		tunnelState = string(s.tunnelClient.Status().State)
	}
	approvalStatus, message := squareRegistrationMetadata(rawMessage)
	return &registerCurrentA2AAgentResponse{
		RegistryAgentID:       registerResp.Agent.ID,
		RegistryAgentName:     registerResp.Agent.Name,
		RegistryAgentHandle:   registerResp.Agent.AgentHandle,
		RegistryAgentPublicID: registerResp.Agent.PublicID,
		RegistryAPIKey:        registerResp.APIKey,
		RegistryURL:           registryURL,
		OwnerEmail:            ownerEmail,
		IntegrationID:         integrationID,
		TunnelState:           tunnelState,
		TunnelNote:            "If the agent is pending owner approval, approve it in Square admin panel before expecting the tunnel to stay connected.",
		ApprovalStatus:        approvalStatus,
		Message:               message,
	}, http.StatusOK, nil
}

func (s *Server) upsertA2ARegistryIntegration(config map[string]string, enabled bool) (string, error) {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		return "", err
	}
	now := time.Now()
	integration := &storage.Integration{
		ID:        uuid.New().String(),
		Provider:  "a2_registry",
		Name:      "A2 Registry",
		Mode:      "duplex",
		Enabled:   enabled,
		Config:    config,
		CreatedAt: now,
		UpdatedAt: now,
	}
	for _, existing := range integrations {
		if existing == nil || existing.Provider != "a2_registry" {
			continue
		}
		integration.ID = existing.ID
		integration.Name = existing.Name
		if strings.TrimSpace(integration.Name) == "" {
			integration.Name = "A2 Registry"
		}
		integration.CreatedAt = existing.CreatedAt
		break
	}
	if err := validateIntegration(*integration); err != nil {
		return "", err
	}
	if err := s.store.SaveIntegration(integration); err != nil {
		return "", err
	}
	return integration.ID, nil
}

func postSquareAgentRegistration(ctx context.Context, registryURL string, registerReq squareRegisterAgentRequest, bearerToken string) (squareRegisterAgentResponse, []byte, int, error) {
	payload, err := json.Marshal(registerReq)
	if err != nil {
		return squareRegisterAgentResponse{}, nil, http.StatusInternalServerError, fmt.Errorf("failed to build registration payload")
	}
	registerCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(registerCtx, http.MethodPost, registryURL+"/agents/register", bytes.NewReader(payload))
	if err != nil {
		return squareRegisterAgentResponse{}, nil, http.StatusInternalServerError, fmt.Errorf("failed to prepare registry request")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(bearerToken) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	}
	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return squareRegisterAgentResponse{}, nil, http.StatusBadGateway, fmt.Errorf("registry request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return squareRegisterAgentResponse{}, respBody, http.StatusBadGateway, fmt.Errorf("registry registration failed: %s", msg)
	}
	var registerResp squareRegisterAgentResponse
	if err := json.Unmarshal(respBody, &registerResp); err != nil {
		return squareRegisterAgentResponse{}, respBody, http.StatusBadGateway, fmt.Errorf("registry returned invalid response")
	}
	return registerResp, respBody, http.StatusOK, nil
}

func squareRegistrationMetadata(raw []byte) (string, string) {
	var payload struct {
		Message string `json:"message"`
		Agent   struct {
			ApprovalStatus string `json:"approval_status"`
		} `json:"agent"`
	}
	_ = json.Unmarshal(raw, &payload)
	return strings.TrimSpace(payload.Agent.ApprovalStatus), strings.TrimSpace(payload.Message)
}

func defaultsAgentHandle(agentName string) string {
	handle := slugifyForA2AgentHandle(agentName)
	hostname, _ := os.Hostname()
	if suffix := stableShortSuffix(hostname); suffix != "" {
		if handle == "" {
			handle = "a2gent"
		}
		handle = strings.Trim(handle, "-") + "-" + suffix
	}
	return handle
}

func stableShortSuffix(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(trimmed))
	return fmt.Sprintf("%06x", h.Sum32()&0xffffff)
}
