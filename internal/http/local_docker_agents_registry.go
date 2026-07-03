package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/go-chi/chi/v5"
)

type registerLocalDockerAgentRequest struct {
	RegistryURL        string  `json:"registry_url"`
	OwnerEmail         string  `json:"owner_email"`
	AgentName          string  `json:"agent_name"`
	AgentHandle        string  `json:"agent_handle"`
	AgentID            string  `json:"agent_id"`
	PublicID           string  `json:"public_id"`
	OrganizationHandle string  `json:"organization_handle"`
	Description        string  `json:"description"`
	NetworkAccess      string  `json:"network_access"`
	EndpointURL        string  `json:"endpoint_url"`
	AgentType          string  `json:"agent_type"`
	Category           string  `json:"category"`
	Discoverable       *bool   `json:"discoverable"`
	OfficialWebsite    string  `json:"official_website"`
	AvatarURL          string  `json:"avatar_url"`
	SupportsAudio      *bool   `json:"supports_audio"`
	SupportsImages     *bool   `json:"supports_images"`
	SupportsVideo      *bool   `json:"supports_video"`
	PricingModel       string  `json:"pricing_model"`
	PricePerRequest    float64 `json:"price_per_request"`
	PricePerInputKB    float64 `json:"price_per_input_kb"`
	PricePerOutputKB   float64 `json:"price_per_output_kb"`
	PricePerSession    float64 `json:"price_per_session"`
	Currency           string  `json:"currency"`
	ConfigureContainer *bool   `json:"configure_container"`
	Transport          string  `json:"transport"`
	SquareGRPCAddr     string  `json:"square_grpc_addr"`
	SquareWSURL        string  `json:"square_ws_url"`
}

type registerLocalDockerAgentResponse struct {
	RegistryAgentID       string `json:"registry_agent_id"`
	RegistryAgentName     string `json:"registry_agent_name"`
	RegistryAgentHandle   string `json:"registry_agent_handle,omitempty"`
	RegistryAgentPublicID string `json:"registry_agent_public_id,omitempty"`
	RegistryAPIKey        string `json:"registry_api_key"`
	RegistryURL           string `json:"registry_url"`
	ContainerName         string `json:"container_name"`
	ContainerID           string `json:"container_id"`
	ContainerHostPort     int    `json:"container_host_port"`
	ContainerAPIURL       string `json:"container_api_url"`
	ContainerConfigured   bool   `json:"container_configured"`
	ContainerIntegration  string `json:"container_integration_id,omitempty"`
	ContainerTunnelState  string `json:"container_tunnel_state,omitempty"`
	ContainerTunnelNote   string `json:"container_tunnel_note,omitempty"`
}

type squareRegisterAgentRequest struct {
	Name               string  `json:"name"`
	AgentHandle        string  `json:"agent_handle"`
	OrganizationHandle string  `json:"organization_handle,omitempty"`
	Description        string  `json:"description,omitempty"`
	NetworkAccess      string  `json:"network_access"`
	EndpointURL        string  `json:"endpoint_url,omitempty"`
	AgentType          string  `json:"agent_type"`
	Category           string  `json:"category"`
	OwnerEmail         string  `json:"owner_email"`
	PricingModel       string  `json:"pricing_model,omitempty"`
	PricePerRequest    float64 `json:"price_per_request,omitempty"`
	PricePerInputKB    float64 `json:"price_per_input_kb,omitempty"`
	PricePerOutputKB   float64 `json:"price_per_output_kb,omitempty"`
	PricePerSession    float64 `json:"price_per_session"`
	Currency           string  `json:"currency"`
	Discoverable       *bool   `json:"discoverable,omitempty"`
	OfficialWebsite    string  `json:"official_website,omitempty"`
	AvatarURL          string  `json:"avatar_url,omitempty"`
	SupportsImages     *bool   `json:"supports_images,omitempty"`
	SupportsAudio      *bool   `json:"supports_audio,omitempty"`
	SupportsVideo      *bool   `json:"supports_video,omitempty"`
}

type squareRegisterAgentResponse struct {
	Agent struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		AgentHandle string `json:"agent_handle"`
		PublicID    string `json:"public_id"`
	} `json:"agent"`
	APIKey string `json:"api_key"`
}

func (s *Server) handleRegisterLocalDockerAgent(w http.ResponseWriter, r *http.Request) {
	containerID := strings.TrimSpace(chi.URLParam(r, "containerID"))
	if !dockerContainerIDPattern.MatchString(containerID) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid container identifier")
		return
	}

	var req registerLocalDockerAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	agent, err := findLocalBruteContainer(r.Context(), containerID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Container not found: "+err.Error())
		return
	}

	resp, statusCode, err := s.registerLocalDockerAgent(r.Context(), agent, req)
	if err != nil {
		s.errorResponse(w, statusCode, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

type localDockerAgentRegistryDefaults struct {
	RegistryURL    string
	APIKey         string
	OwnerEmail     string
	Transport      string
	SquareGRPCAddr string
	SquareWSURL    string
}

func (s *Server) localDockerAgentRegistryDefaults() localDockerAgentRegistryDefaults {
	defaults := localDockerAgentRegistryDefaults{
		Transport:      "grpc",
		SquareGRPCAddr: defaultSquareGRPCAddr,
	}
	if s == nil || s.store == nil {
		return defaults
	}
	if settings, err := s.store.GetSettings(); err == nil && settings != nil {
		if registryURL := strings.TrimSpace(settings[a2aRegistryURLSettingKey]); registryURL != "" {
			defaults.RegistryURL = registryURL
		}
	}
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		return defaults
	}
	for _, existing := range integrations {
		if existing == nil || existing.Provider != "a2_registry" {
			continue
		}
		if v := strings.TrimSpace(existing.Config["registry_url"]); v != "" {
			defaults.RegistryURL = v
		}
		if v := strings.TrimSpace(existing.Config["api_key"]); v != "" {
			defaults.APIKey = v
		}
		if v := strings.TrimSpace(existing.Config["owner_email"]); v != "" {
			defaults.OwnerEmail = v
		}
		if v := strings.TrimSpace(existing.Config["transport"]); v != "" {
			defaults.Transport = strings.ToLower(v)
		}
		if v := strings.TrimSpace(existing.Config["square_grpc_addr"]); v != "" {
			defaults.SquareGRPCAddr = v
		}
		if v := strings.TrimSpace(existing.Config["square_ws_url"]); v != "" {
			defaults.SquareWSURL = v
		}
		break
	}
	return defaults
}

func resolveLocalDockerAgentOwnerEmailFromRegistry(ctx context.Context, registryURL string, apiKey string) string {
	registryURL = strings.TrimRight(strings.TrimSpace(registryURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	if registryURL == "" || apiKey == "" {
		return ""
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(resolveCtx, http.MethodGet, registryURL+"/agents/me", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var payload struct {
		OwnerEmail string `json:"owner_email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.OwnerEmail)
}

func (s *Server) registerLocalDockerAgent(ctx context.Context, agent *LocalDockerAgent, req registerLocalDockerAgentRequest) (*registerLocalDockerAgentResponse, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if agent == nil {
		return nil, http.StatusNotFound, fmt.Errorf("container not found")
	}
	if agent.HostPort == 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("container does not expose port 8080 on host")
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
		ownerEmail = strings.TrimSpace(defaults.OwnerEmail)
	}
	if ownerEmail == "" {
		ownerEmail = resolveLocalDockerAgentOwnerEmailFromRegistry(ctx, registryURL, defaults.APIKey)
	}
	if ownerEmail == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("owner_email is required; configure the parent a2_registry integration, let its API key resolve /agents/me owner_email, or set registry.owner_email in YAML")
	}

	agentName := strings.TrimSpace(req.AgentName)
	if agentName == "" {
		agentName = firstNonEmptyLocalAgentString(agent.Labels["a2gent.agent_name"], agent.Name)
	}
	description := firstNonEmptyLocalAgentString(req.Description, agent.Labels["a2gent.agent_description"])
	if description == "" {
		description = "Local dockerized Brute agent"
	}

	organizationHandle := strings.TrimSpace(req.OrganizationHandle)
	agentHandle := firstNonEmptyLocalAgentString(req.AgentHandle, req.AgentID, req.PublicID)
	if strings.Contains(agentHandle, "/") {
		parts := strings.SplitN(agentHandle, "/", 2)
		if organizationHandle == "" {
			organizationHandle = strings.TrimSpace(parts[0])
		}
		agentHandle = strings.TrimSpace(parts[1])
	}
	if agentHandle == "" {
		agentHandle = agent.Name
	}
	agentHandle = slugifyForA2AgentHandle(agentHandle)
	if agentHandle == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("agent_handle is required")
	}
	organizationHandle = slugifyForA2AgentHandle(organizationHandle)

	networkAccess := strings.TrimSpace(req.NetworkAccess)
	if networkAccess == "" {
		networkAccess = "behind_nat"
	}
	agentType := strings.TrimSpace(req.AgentType)
	if agentType == "" {
		agentType = "personal"
	}
	if organizationHandle != "" && agentType == "personal" {
		// WHY: Square treats organization-scoped handles as business agents.
		agentType = "business"
	}
	category := firstNonEmptyLocalAgentString(req.Category, agent.Labels["a2gent.agent_category"])
	if category == "" {
		category = "other"
	}
	avatarURL := firstNonEmptyLocalAgentString(req.AvatarURL, agent.Labels["a2gent.agent_avatar_url"], agent.Labels["a2gent.agent_icon_url"])
	localAvatarPath := localDockerAgentAvatarFilePath(avatarURL)
	registrationAvatarURL := strings.TrimSpace(avatarURL)
	if localAvatarPath != "" {
		// WHY: Square registry stores public URLs only. Local Soul asset URLs are
		// uploaded as registry-hosted avatars after registration instead.
		registrationAvatarURL = ""
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
		NetworkAccess:      networkAccess,
		EndpointURL:        strings.TrimSpace(req.EndpointURL),
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
		AvatarURL:          registrationAvatarURL,
		SupportsImages:     &supportsImages,
		SupportsAudio:      &supportsAudio,
		SupportsVideo:      &supportsVideo,
	}

	registerResp, _, statusCode, err := postSquareAgentRegistration(ctx, registryURL, registerReq, defaults.APIKey)
	if err != nil {
		return nil, statusCode, err
	}

	if localAvatarPath != "" && strings.TrimSpace(registerResp.Agent.ID) != "" && strings.TrimSpace(registerResp.APIKey) != "" {
		if uploadErr := uploadLocalDockerAgentAvatar(ctx, registryURL, registerResp.Agent.ID, registerResp.APIKey, localAvatarPath); uploadErr != nil {
			// Non-fatal: metadata sync should still configure the agent tunnel even if
			// a local avatar file disappeared or Square avatar storage is unavailable.
			logging.Warn("Failed to upload local Docker agent avatar for %s: %v", agentName, uploadErr)
		}
	}
	httpClient := &http.Client{Timeout: 15 * time.Second}
	configureContainer := true
	if req.ConfigureContainer != nil {
		configureContainer = *req.ConfigureContainer
	}
	containerConfigured := false
	containerIntegrationID := ""
	containerTunnelState := ""
	containerTunnelNote := ""
	if configureContainer {
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

		integrationReq := IntegrationRequest{
			Provider: "a2_registry",
			Name:     "A2 Registry",
			Mode:     "duplex",
			Enabled:  boolPtr(true),
			Config: map[string]string{
				"api_key":          registerResp.APIKey,
				"owner_email":      ownerEmail,
				"registry_url":     registryURL,
				"transport":        transport,
				"square_grpc_addr": squareGRPCAddr,
				"square_ws_url":    squareWSURL,
			},
		}

		integrationPayload, err := json.Marshal(integrationReq)
		if err == nil {
			containerURL := fmt.Sprintf("http://127.0.0.1:%d", agent.HostPort)
			integrationID, upsertErr := upsertContainerA2RegistryIntegration(ctx, httpClient, containerURL, integrationPayload)
			if upsertErr == nil {
				containerConfigured = true
				containerIntegrationID = integrationID
				_, _ = reconnectContainerTunnel(ctx, httpClient, containerURL)
				if state, note, statusErr := readContainerTunnelStatus(ctx, httpClient, containerURL); statusErr == nil {
					containerTunnelState = state
					containerTunnelNote = note
				}
			} else {
				containerTunnelNote = upsertErr.Error()
			}
		}
	}

	return &registerLocalDockerAgentResponse{
		RegistryAgentID:       registerResp.Agent.ID,
		RegistryAgentName:     registerResp.Agent.Name,
		RegistryAgentHandle:   registerResp.Agent.AgentHandle,
		RegistryAgentPublicID: registerResp.Agent.PublicID,
		RegistryAPIKey:        registerResp.APIKey,
		RegistryURL:           registryURL,
		ContainerName:         agent.Name,
		ContainerID:           agent.ID,
		ContainerHostPort:     agent.HostPort,
		ContainerAPIURL:       agent.APIURL,
		ContainerConfigured:   containerConfigured,
		ContainerIntegration:  containerIntegrationID,
		ContainerTunnelState:  containerTunnelState,
		ContainerTunnelNote:   containerTunnelNote,
	}, http.StatusOK, nil
}

func localDockerAgentAvatarFilePath(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "/") {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil || u == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if (host == "localhost" || host == "127.0.0.1" || host == "::1") && strings.HasPrefix(u.Path, "/assets/images") {
		if p := strings.TrimSpace(u.Query().Get("path")); p != "" && strings.HasPrefix(p, "/") {
			return p
		}
	}
	return ""
}

func uploadLocalDockerAgentAvatar(ctx context.Context, registryURL string, agentID string, apiKey string, avatarPath string) error {
	avatarPath = strings.TrimSpace(avatarPath)
	if avatarPath == "" {
		return nil
	}
	file, err := os.Open(avatarPath)
	if err != nil {
		return err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("avatar", filepath.Base(avatarPath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	uploadCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, strings.TrimRight(registryURL, "/")+"/owner/agents/"+agentID+"/avatar", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("registry avatar upload failed: %s", msg)
	}
	return nil
}

func firstNonEmptyLocalAgentString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func slugifyForA2AgentHandle(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	var out strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		isValid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if isValid {
			out.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func upsertContainerA2RegistryIntegration(ctx context.Context, client *http.Client, containerURL string, integrationPayload []byte) (string, error) {
	listReq, err := http.NewRequestWithContext(ctx, http.MethodGet, containerURL+"/integrations", nil)
	if err != nil {
		return "", err
	}
	listResp, err := client.Do(listReq)
	if err != nil {
		return "", err
	}
	listBody, _ := io.ReadAll(listResp.Body)
	_ = listResp.Body.Close()
	if listResp.StatusCode < 200 || listResp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to list container integrations: %s", strings.TrimSpace(string(listBody)))
	}

	var existing []IntegrationResponse
	_ = json.Unmarshal(listBody, &existing)
	for _, integration := range existing {
		if integration.Provider != "a2_registry" {
			continue
		}
		updateReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPut, containerURL+"/integrations/"+integration.ID, bytes.NewReader(integrationPayload))
		if reqErr != nil {
			return "", reqErr
		}
		updateReq.Header.Set("Content-Type", "application/json")
		updateResp, doErr := client.Do(updateReq)
		if doErr != nil {
			return "", doErr
		}
		updateBody, _ := io.ReadAll(updateResp.Body)
		_ = updateResp.Body.Close()
		if updateResp.StatusCode < 200 || updateResp.StatusCode >= 300 {
			return "", fmt.Errorf("failed to update container integration: %s", strings.TrimSpace(string(updateBody)))
		}
		var updated IntegrationResponse
		if json.Unmarshal(updateBody, &updated) == nil && updated.ID != "" {
			return updated.ID, nil
		}
		return integration.ID, nil
	}

	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, containerURL+"/integrations", bytes.NewReader(integrationPayload))
	if err != nil {
		return "", err
	}
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := client.Do(createReq)
	if err != nil {
		return "", err
	}
	createBody, _ := io.ReadAll(createResp.Body)
	_ = createResp.Body.Close()
	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to create container integration: %s", strings.TrimSpace(string(createBody)))
	}
	var created IntegrationResponse
	if json.Unmarshal(createBody, &created) == nil && created.ID != "" {
		return created.ID, nil
	}
	return "", nil
}

func readContainerTunnelStatus(ctx context.Context, client *http.Client, containerURL string) (string, string, error) {
	statusReq, err := http.NewRequestWithContext(ctx, http.MethodGet, containerURL+"/integrations/a2_registry/tunnel-status", nil)
	if err != nil {
		return "", "", err
	}
	statusResp, err := client.Do(statusReq)
	if err != nil {
		return "", "", err
	}
	defer statusResp.Body.Close()
	statusBody, _ := io.ReadAll(statusResp.Body)
	if statusResp.StatusCode < 200 || statusResp.StatusCode >= 300 {
		return "", "", fmt.Errorf("failed to get tunnel status: %s", strings.TrimSpace(string(statusBody)))
	}
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(statusBody, &payload); err != nil {
		return "", "", err
	}
	state := strings.TrimSpace(payload.State)
	note := ""
	if state != "connected" {
		note = "Container integration saved, but tunnel is not connected yet."
	}
	return state, note, nil
}

func reconnectContainerTunnel(ctx context.Context, client *http.Client, containerURL string) (string, error) {
	reconnectReq, err := http.NewRequestWithContext(ctx, http.MethodPost, containerURL+"/integrations/a2_registry/tunnel-reconnect", bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	reconnectReq.Header.Set("Content-Type", "application/json")
	reconnectResp, err := client.Do(reconnectReq)
	if err != nil {
		return "", err
	}
	defer reconnectResp.Body.Close()
	body, _ := io.ReadAll(reconnectResp.Body)
	if reconnectResp.StatusCode < 200 || reconnectResp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to reconnect tunnel: %s", strings.TrimSpace(string(body)))
	}
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil
	}
	return strings.TrimSpace(payload.State), nil
}
