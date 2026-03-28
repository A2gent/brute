package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	defaultLocalAgentImage      = "a2gent-brute:latest"
	defaultLocalAgentBasePort   = 18080
	defaultLocalAgentMaxPort    = 18180
	defaultSquareGRPCAddr       = "a2gent.net:9001"
	defaultLocalRegistryURL     = "http://localhost:5174"
	localAgentManagerLabelKey   = "a2gent.local_agent"
	localAgentManagerLabelValue = "true"
)

var dockerContainerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type dockerPSRow struct {
	ID         string `json:"ID"`
	Image      string `json:"Image"`
	Command    string `json:"Command"`
	CreatedAt  string `json:"CreatedAt"`
	RunningFor string `json:"RunningFor"`
	Ports      string `json:"Ports"`
	Status     string `json:"Status"`
	State      string `json:"State"`
	Names      string `json:"Names"`
	Labels     string `json:"Labels"`
}

type LocalDockerAgent struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	State     string            `json:"state"`
	Status    string            `json:"status"`
	CreatedAt string            `json:"created_at,omitempty"`
	Ports     string            `json:"ports,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Managed   bool              `json:"managed"`
	Running   bool              `json:"running"`
	HostPort  int               `json:"host_port,omitempty"`
	APIURL    string            `json:"api_url,omitempty"`
}

type createLocalDockerAgentRequest struct {
	Name             string `json:"name"`
	Image            string `json:"image"`
	HostPort         int    `json:"host_port"`
	LMStudioBaseURL  string `json:"lm_studio_base_url"`
	AgentKind        string `json:"agent_kind"`
	SystemPrompt     string `json:"system_prompt"`
	SessionID        string `json:"session_id"`
	ProjectID        string `json:"project_id"`
	ProjectMountMode string `json:"project_mount_mode"`
}

type localDockerAgentCreateResult struct {
	Agent   *LocalDockerAgent
	Name    string
	Warning string
}

type buildLocalDockerAgentImageRequest struct {
	Image   string `json:"image"`
	NoCache bool   `json:"no_cache"`
}

type removeLocalDockerAgentRequest struct {
	Force bool `json:"force"`
}

type registerLocalDockerAgentRequest struct {
	RegistryURL        string `json:"registry_url"`
	OwnerEmail         string `json:"owner_email"`
	AgentName          string `json:"agent_name"`
	Description        string `json:"description"`
	ConfigureContainer *bool  `json:"configure_container"`
	SquareGRPCAddr     string `json:"square_grpc_addr"`
}

type registerLocalDockerAgentResponse struct {
	RegistryAgentID      string `json:"registry_agent_id"`
	RegistryAgentName    string `json:"registry_agent_name"`
	RegistryAPIKey       string `json:"registry_api_key"`
	RegistryURL          string `json:"registry_url"`
	ContainerName        string `json:"container_name"`
	ContainerID          string `json:"container_id"`
	ContainerHostPort    int    `json:"container_host_port"`
	ContainerAPIURL      string `json:"container_api_url"`
	ContainerConfigured  bool   `json:"container_configured"`
	ContainerIntegration string `json:"container_integration_id,omitempty"`
	ContainerTunnelState string `json:"container_tunnel_state,omitempty"`
	ContainerTunnelNote  string `json:"container_tunnel_note,omitempty"`
}

type squareRegisterAgentRequest struct {
	Name            string  `json:"name"`
	Description     string  `json:"description,omitempty"`
	NetworkAccess   string  `json:"network_access"`
	AgentType       string  `json:"agent_type"`
	OwnerEmail      string  `json:"owner_email"`
	PricePerSession float64 `json:"price_per_session"`
	Currency        string  `json:"currency"`
	Discoverable    *bool   `json:"discoverable,omitempty"`
	SupportsImages  *bool   `json:"supports_images,omitempty"`
	SupportsAudio   *bool   `json:"supports_audio,omitempty"`
}

type squareRegisterAgentResponse struct {
	Agent struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
	APIKey string `json:"api_key"`
}

func (s *Server) handleListLocalDockerAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := listLocalBruteContainers(r.Context())
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"agents": agents})
}

func (s *Server) handleCreateLocalDockerAgent(w http.ResponseWriter, r *http.Request) {
	var req createLocalDockerAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	result, statusCode, err := s.createLocalDockerAgent(r.Context(), req)
	if err != nil {
		s.errorResponse(w, statusCode, err.Error())
		return
	}
	if result.Agent != nil {
		s.jsonResponse(w, http.StatusCreated, result.Agent)
		return
	}
	s.jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"name":    result.Name,
		"status":  "started",
		"warning": result.Warning,
	})
}

func (s *Server) createLocalDockerAgent(ctx context.Context, req createLocalDockerAgentRequest) (*localDockerAgentCreateResult, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = fmt.Sprintf("a2gent-local-%d", time.Now().Unix())
	}
	if !dockerContainerIDPattern.MatchString(name) {
		return nil, http.StatusBadRequest, fmt.Errorf("container name may contain only letters, numbers, ., _, and -")
	}

	image := strings.TrimSpace(req.Image)
	if image == "" {
		image = strings.TrimSpace(os.Getenv("A2GENT_LOCAL_AGENT_IMAGE"))
	}
	if image == "" {
		image = defaultLocalAgentImage
	}

	hostPort := req.HostPort
	if hostPort <= 0 {
		var err error
		hostPort, err = findAvailablePort(defaultLocalAgentBasePort, defaultLocalAgentMaxPort)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("no available host port found in local agent range")
		}
	}
	if hostPort < 1 || hostPort > 65535 {
		return nil, http.StatusBadRequest, fmt.Errorf("host_port must be between 1 and 65535")
	}

	lmStudioBaseURL := strings.TrimSpace(req.LMStudioBaseURL)
	if lmStudioBaseURL == "" {
		lmStudioBaseURL = strings.TrimSpace(os.Getenv("LM_STUDIO_BASE_URL"))
	}
	if lmStudioBaseURL == "" && !s.llmProxyEnabled() {
		lmStudioBaseURL = "http://host.docker.internal:1234/v1"
	}
	if s.llmProxyEnabled() {
		lmStudioBaseURL = fmt.Sprintf("http://host.docker.internal:%d/v1", s.port)
	}
	agentKind := strings.TrimSpace(req.AgentKind)
	agentKindLabel := sanitizeDockerLabelValue(agentKind)
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	sessionID := strings.TrimSpace(req.SessionID)
	sessionIDLabel := sanitizeDockerLabelValue(sessionID)
	projectID := strings.TrimSpace(req.ProjectID)
	projectMountMode := strings.ToLower(strings.TrimSpace(req.ProjectMountMode))
	if projectMountMode == "" {
		projectMountMode = "ro"
	}
	if projectMountMode != "ro" && projectMountMode != "rw" {
		return nil, http.StatusBadRequest, fmt.Errorf("project_mount_mode must be either ro or rw")
	}
	if projectID == "" && strings.TrimSpace(req.ProjectMountMode) != "" {
		return nil, http.StatusBadRequest, fmt.Errorf("project_id is required when project_mount_mode is set")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to resolve home directory")
	}
	dataDir := filepath.Join(home, ".a2gent-data", "local-agents", name)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to prepare local agent data directory: %w", err)
	}

	projectRoot := ""
	if projectID != "" {
		resolvedRoot, err := s.resolveProjectRootFolder(projectID)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid project_id: %w", err)
		}
		projectRoot = resolvedRoot
	}

	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	proxyBaseURL := fmt.Sprintf("http://host.docker.internal:%d/v1", s.port)
	args := []string{
		"run", "-d",
		"--name", name,
		"--label", localAgentManagerLabelKey + "=" + localAgentManagerLabelValue,
		"--label", "a2gent.role=brute",
		"--publish", fmt.Sprintf("%d:8080", hostPort),
		"--volume", dataDir + ":/data",
		"--env", "HOME=/data",
		"--env", "AAGENT_DATA_PATH=/data",
		"--env", "LM_STUDIO_BASE_URL=" + lmStudioBaseURL,
		"--env", disableToolsByDefaultSettingKey + "=true",
	}
	if projectID != "" {
		projectIDLabel := sanitizeDockerLabelValue(projectID)
		if projectIDLabel != "" {
			args = append(args, "--label", "a2gent.project_id="+projectIDLabel)
		}
		args = append(args, "--label", "a2gent.project_mount_mode="+projectMountMode)
		projectMount := projectRoot + ":/workspace"
		if projectMountMode == "ro" {
			projectMount += ":ro"
		}
		args = append(args, "--volume", projectMount)
	}
	if agentKind != "" {
		if agentKindLabel != "" {
			args = append(args, "--label", "a2gent.agent_kind="+agentKindLabel)
		}
		args = append(args, "--env", "A2GENT_AGENT_KIND="+agentKind)
	}
	if systemPrompt != "" {
		args = append(args, "--env", "AAGENT_SYSTEM_PROMPT="+systemPrompt)
	}
	if sessionID != "" {
		if sessionIDLabel != "" {
			args = append(args, "--label", "a2gent.session_id="+sessionIDLabel)
		}
		args = append(args, "--env", "A2GENT_PARENT_SESSION_ID="+sessionID)
	}
	if s.llmProxyEnabled() {
		// Route child agent traffic through the parent's OpenAI-compatible proxy.
		args = append(args,
			"--env", "AAGENT_PROVIDER=lmstudio",
			"--env", "A2GENT_PARENT_PROXY_URL="+proxyBaseURL,
			"--env", "LM_STUDIO_BASE_URL="+proxyBaseURL+"/providers/lmstudio",
			"--env", "OPENAI_API_KEY=a2gent-proxy",
			"--env", "OPENAI_BASE_URL="+proxyBaseURL+"/providers/openai",
			"--env", "KIMI_API_KEY=a2gent-proxy",
			"--env", "KIMI_BASE_URL="+proxyBaseURL+"/providers/kimi",
			"--env", "GOOGLE_API_KEY=a2gent-proxy",
			"--env", "GOOGLE_BASE_URL="+proxyBaseURL+"/providers/google",
			"--env", "OPENROUTER_API_KEY=a2gent-proxy",
			"--env", "OPENROUTER_BASE_URL="+proxyBaseURL+"/providers/openrouter",
			"--env", "ANTHROPIC_API_KEY=a2gent-proxy",
		)
	}
	// Docker port mapping publishes hostPort -> container:8080, so force the
	// child server to bind to 8080 inside the container.
	args = append(args, image, "server", "--port", "8080")
	_, err = runCommand(runCtx, "docker", args...)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("failed to start local agent container: %w", err)
	}

	agent, err := findLocalBruteContainer(ctx, name)
	if err != nil {
		return &localDockerAgentCreateResult{
			Name:    name,
			Warning: "Container started but details could not be loaded: " + err.Error(),
		}, http.StatusCreated, nil
	}
	return &localDockerAgentCreateResult{Agent: agent}, http.StatusCreated, nil
}

func (s *Server) handleBuildLocalDockerAgentImage(w http.ResponseWriter, r *http.Request) {
	var req buildLocalDockerAgentImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	image := strings.TrimSpace(req.Image)
	if image == "" {
		image = strings.TrimSpace(os.Getenv("A2GENT_LOCAL_AGENT_IMAGE"))
	}
	if image == "" {
		image = defaultLocalAgentImage
	}

	dockerfilePath, contextDir, err := resolveLocalAgentDockerBuildPaths()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to resolve Docker build configuration: "+err.Error())
		return
	}

	args := []string{"build", "--tag", image, "--file", dockerfilePath}
	if req.NoCache {
		args = append(args, "--no-cache")
	}
	args = append(args, contextDir)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	buildOutput, err := runCommand(ctx, "docker", args...)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to build local agent image: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status":      "built",
		"image":       image,
		"dockerfile":  dockerfilePath,
		"context_dir": contextDir,
		"output":      buildOutput,
	})
}

func (s *Server) handleStartLocalDockerAgent(w http.ResponseWriter, r *http.Request) {
	containerID := strings.TrimSpace(chi.URLParam(r, "containerID"))
	if !dockerContainerIDPattern.MatchString(containerID) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid container identifier")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if _, err := runCommand(ctx, "docker", "start", containerID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to start container: "+err.Error())
		return
	}
	agent, err := findLocalBruteContainer(r.Context(), containerID)
	if err != nil {
		s.jsonResponse(w, http.StatusOK, map[string]interface{}{
			"container": containerID,
			"status":    "started",
			"warning":   "Container started, but details could not be loaded: " + err.Error(),
		})
		return
	}
	s.jsonResponse(w, http.StatusOK, agent)
}

func (s *Server) handleStopLocalDockerAgent(w http.ResponseWriter, r *http.Request) {
	containerID := strings.TrimSpace(chi.URLParam(r, "containerID"))
	if !dockerContainerIDPattern.MatchString(containerID) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid container identifier")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := runCommand(ctx, "docker", "stop", containerID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to stop container: "+err.Error())
		return
	}
	agent, err := findLocalBruteContainer(r.Context(), containerID)
	if err != nil {
		s.jsonResponse(w, http.StatusOK, map[string]interface{}{
			"container": containerID,
			"status":    "stopped",
			"warning":   "Container stopped, but details could not be loaded: " + err.Error(),
		})
		return
	}
	s.jsonResponse(w, http.StatusOK, agent)
}

func (s *Server) handleRemoveLocalDockerAgent(w http.ResponseWriter, r *http.Request) {
	containerID := strings.TrimSpace(chi.URLParam(r, "containerID"))
	if !dockerContainerIDPattern.MatchString(containerID) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid container identifier")
		return
	}
	var req removeLocalDockerAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	args := []string{"rm"}
	if req.Force {
		args = append(args, "-f")
	}
	args = append(args, containerID)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := runCommand(ctx, "docker", args...); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to remove container: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "removed", "container": containerID})
}

func (s *Server) handleLocalDockerAgentLogs(w http.ResponseWriter, r *http.Request) {
	containerID := strings.TrimSpace(chi.URLParam(r, "containerID"))
	if !dockerContainerIDPattern.MatchString(containerID) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid container identifier")
		return
	}
	tail := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 5000 {
			tail = parsed
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	logs, err := runCommand(ctx, "docker", "logs", "--tail", strconv.Itoa(tail), containerID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read container logs: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"container": containerID,
		"tail":      tail,
		"logs":      logs,
	})
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
	ownerEmail := strings.TrimSpace(req.OwnerEmail)
	if ownerEmail == "" {
		s.errorResponse(w, http.StatusBadRequest, "owner_email is required")
		return
	}

	agent, err := findLocalBruteContainer(r.Context(), containerID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Container not found: "+err.Error())
		return
	}
	if agent.HostPort == 0 {
		s.errorResponse(w, http.StatusBadRequest, "Container does not expose port 8080 on host")
		return
	}

	registryURL := strings.TrimRight(strings.TrimSpace(req.RegistryURL), "/")
	if registryURL == "" {
		registryURL = defaultLocalRegistryURL
	}
	agentName := strings.TrimSpace(req.AgentName)
	if agentName == "" {
		agentName = agent.Name
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = "Local dockerized Brute agent"
	}

	discoverable := true
	supportsImages := true
	supportsAudio := true
	registerReq := squareRegisterAgentRequest{
		Name:            agentName,
		Description:     description,
		NetworkAccess:   "behind_nat",
		AgentType:       "personal",
		OwnerEmail:      ownerEmail,
		PricePerSession: 0.001,
		Currency:        "USD",
		Discoverable:    &discoverable,
		SupportsImages:  &supportsImages,
		SupportsAudio:   &supportsAudio,
	}

	payload, err := json.Marshal(registerReq)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to build registration payload")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, registryURL+"/agents/register", bytes.NewReader(payload))
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to prepare registry request")
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Registry request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		s.errorResponse(w, http.StatusBadGateway, "Registry registration failed: "+msg)
		return
	}

	var registerResp squareRegisterAgentResponse
	if err := json.Unmarshal(respBody, &registerResp); err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Registry returned invalid response")
		return
	}

	configureContainer := true
	if req.ConfigureContainer != nil {
		configureContainer = *req.ConfigureContainer
	}
	containerConfigured := false
	containerIntegrationID := ""
	containerTunnelState := ""
	containerTunnelNote := ""
	if configureContainer {
		transport := "grpc"
		squareGRPCAddr := defaultSquareGRPCAddr
		squareWSURL := ""
		if integrations, listErr := s.store.ListIntegrations(); listErr == nil {
			for _, existing := range integrations {
				if existing == nil || existing.Provider != "a2_registry" {
					continue
				}
				if t := strings.TrimSpace(existing.Config["transport"]); t != "" {
					transport = t
				}
				if v := strings.TrimSpace(existing.Config["square_grpc_addr"]); v != "" {
					squareGRPCAddr = v
				}
				if v := strings.TrimSpace(existing.Config["square_ws_url"]); v != "" {
					squareWSURL = v
				}
				break
			}
		}
		if requested := strings.TrimSpace(req.SquareGRPCAddr); requested != "" {
			squareGRPCAddr = requested
		}
		if transport != "grpc" && transport != "websocket" {
			transport = "grpc"
		}

		integrationReq := IntegrationRequest{
			Provider: "a2_registry",
			Name:     "A2 Registry",
			Mode:     "duplex",
			Enabled:  boolPtr(true),
			Config: map[string]string{
				"api_key":          registerResp.APIKey,
				"owner_email":      ownerEmail,
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

	s.jsonResponse(w, http.StatusOK, registerLocalDockerAgentResponse{
		RegistryAgentID:      registerResp.Agent.ID,
		RegistryAgentName:    registerResp.Agent.Name,
		RegistryAPIKey:       registerResp.APIKey,
		RegistryURL:          registryURL,
		ContainerName:        agent.Name,
		ContainerID:          agent.ID,
		ContainerHostPort:    agent.HostPort,
		ContainerAPIURL:      agent.APIURL,
		ContainerConfigured:  containerConfigured,
		ContainerIntegration: containerIntegrationID,
		ContainerTunnelState: containerTunnelState,
		ContainerTunnelNote:  containerTunnelNote,
	})
}

func resolveLocalAgentDockerBuildPaths() (string, string, error) {
	if rawPath := strings.TrimSpace(os.Getenv("A2GENT_LOCAL_AGENT_DOCKERFILE")); rawPath != "" {
		dockerfilePath := rawPath
		if !filepath.IsAbs(dockerfilePath) {
			cwd, err := os.Getwd()
			if err != nil {
				return "", "", fmt.Errorf("failed to resolve current directory: %w", err)
			}
			dockerfilePath = filepath.Join(cwd, dockerfilePath)
		}
		dockerfilePath = filepath.Clean(dockerfilePath)
		if !fileExists(dockerfilePath) {
			return "", "", fmt.Errorf("dockerfile not found at %q", dockerfilePath)
		}

		contextDir := strings.TrimSpace(os.Getenv("A2GENT_LOCAL_AGENT_DOCKER_CONTEXT"))
		if contextDir == "" {
			contextDir = filepath.Dir(dockerfilePath)
		}
		if !filepath.IsAbs(contextDir) {
			cwd, err := os.Getwd()
			if err != nil {
				return "", "", fmt.Errorf("failed to resolve current directory: %w", err)
			}
			contextDir = filepath.Join(cwd, contextDir)
		}
		contextDir = filepath.Clean(contextDir)
		if !dirExists(contextDir) {
			return "", "", fmt.Errorf("docker build context directory not found at %q", contextDir)
		}
		return dockerfilePath, contextDir, nil
	}

	candidates := make([]string, 0, 4)
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "brute", "Dockerfile"))
		candidates = append(candidates, filepath.Join(cwd, "Dockerfile"))
	}
	if executable, err := os.Executable(); err == nil {
		execDir := filepath.Dir(executable)
		candidates = append(candidates, filepath.Join(execDir, "..", "brute", "Dockerfile"))
		candidates = append(candidates, filepath.Join(execDir, "Dockerfile"))
	}

	for _, candidate := range candidates {
		cleaned := filepath.Clean(candidate)
		if fileExists(cleaned) {
			return cleaned, filepath.Dir(cleaned), nil
		}
	}

	return "", "", fmt.Errorf(
		"unable to find Dockerfile (checked default locations). Set A2GENT_LOCAL_AGENT_DOCKERFILE and optional A2GENT_LOCAL_AGENT_DOCKER_CONTEXT",
	)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func boolPtr(v bool) *bool {
	return &v
}

func runCommand(ctx context.Context, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, trimmed)
	}
	return trimmed, nil
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

func parseDockerLabels(raw string) map[string]string {
	labels := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		if key != "" {
			labels[key] = value
		}
	}
	return labels
}

func sanitizeDockerLabelValue(value string) string {
	if value == "" {
		return ""
	}
	normalized := strings.NewReplacer(",", "_", "\n", " ", "\r", " ").Replace(value)
	return strings.TrimSpace(normalized)
}

func parseHostPort(ports string) int {
	if ports == "" {
		return 0
	}
	chunks := strings.Split(ports, ",")
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		idx := strings.Index(chunk, "->8080/tcp")
		if idx == -1 {
			continue
		}
		prefix := chunk[:idx]
		colon := strings.LastIndex(prefix, ":")
		if colon == -1 || colon+1 >= len(prefix) {
			continue
		}
		portStr := strings.TrimSpace(prefix[colon+1:])
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
			return port
		}
	}
	return 0
}

func isBruteContainer(row dockerPSRow, labels map[string]string) bool {
	if strings.EqualFold(labels[localAgentManagerLabelKey], localAgentManagerLabelValue) {
		return true
	}
	img := strings.ToLower(strings.TrimSpace(row.Image))
	name := strings.ToLower(strings.TrimSpace(row.Names))
	if strings.Contains(img, "a2gent-brute") || strings.Contains(img, "/brute") {
		return true
	}
	if strings.Contains(name, "a2gent-brute") || strings.Contains(name, "brute") {
		return true
	}
	if service := strings.ToLower(strings.TrimSpace(labels["com.docker.compose.service"])); service == "brute" || service == "brute-tui" {
		return true
	}
	return false
}

func listLocalBruteContainers(ctx context.Context) ([]LocalDockerAgent, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := runCommand(cmdCtx, "docker", "ps", "-a", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("failed to list docker containers: %w", err)
	}
	if strings.TrimSpace(output) == "" {
		return []LocalDockerAgent{}, nil
	}
	lines := strings.Split(output, "\n")
	agents := make([]LocalDockerAgent, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row dockerPSRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		labels := parseDockerLabels(row.Labels)
		if !isBruteContainer(row, labels) {
			continue
		}
		hostPort := parseHostPort(row.Ports)
		running := strings.EqualFold(row.State, "running")
		agent := LocalDockerAgent{
			ID:        row.ID,
			Name:      row.Names,
			Image:     row.Image,
			State:     row.State,
			Status:    row.Status,
			CreatedAt: row.CreatedAt,
			Ports:     row.Ports,
			Labels:    labels,
			Managed:   strings.EqualFold(labels[localAgentManagerLabelKey], localAgentManagerLabelValue),
			Running:   running,
			HostPort:  hostPort,
		}
		if hostPort > 0 {
			agent.APIURL = fmt.Sprintf("http://127.0.0.1:%d", hostPort)
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

func findLocalBruteContainer(ctx context.Context, containerID string) (*LocalDockerAgent, error) {
	agents, err := listLocalBruteContainers(ctx)
	if err != nil {
		return nil, err
	}
	needle := strings.TrimSpace(containerID)
	for i := range agents {
		if agents[i].ID == needle || agents[i].Name == needle || strings.HasPrefix(agents[i].ID, needle) {
			return &agents[i], nil
		}
	}
	return nil, fmt.Errorf("container %q not found", containerID)
}

func findAvailablePort(start, end int) (int, error) {
	for port := start; port <= end; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no available ports in range %d-%d", start, end)
}
