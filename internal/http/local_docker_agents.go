package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/go-chi/chi/v5"
)

const (
	defaultLocalAgentImage      = "a2gent-brute:latest"
	defaultLocalAgentBasePort   = 18080
	defaultLocalAgentMaxPort    = 18180
	defaultSquareGRPCAddr       = "a2gent.net:9001"
	defaultLocalRegistryURL     = "https://a2gent.net"
	localAgentManagerLabelKey   = "a2gent.local_agent"
	localAgentManagerLabelValue = "true"
)

type LocalDockerAgent struct {
	ID             string                         `json:"id"`
	Name           string                         `json:"name"`
	Image          string                         `json:"image"`
	State          string                         `json:"state"`
	Status         string                         `json:"status"`
	CreatedAt      string                         `json:"created_at,omitempty"`
	Ports          string                         `json:"ports,omitempty"`
	Labels         map[string]string              `json:"labels,omitempty"`
	Managed        bool                           `json:"managed"`
	Running        bool                           `json:"running"`
	HostPort       int                            `json:"host_port,omitempty"`
	APIURL         string                         `json:"api_url,omitempty"`
	StartupSession *localDockerAgentStartupResult `json:"startup_session,omitempty"`
}

type createLocalDockerAgentRequest struct {
	Name             string                                `json:"name"`
	Image            string                                `json:"image"`
	HostPort         int                                   `json:"host_port"`
	LMStudioBaseURL  string                                `json:"lm_studio_base_url"`
	AgentKind        string                                `json:"agent_kind"`
	SystemPrompt     string                                `json:"system_prompt"`
	InitialPrompt    string                                `json:"initial_prompt"`
	SessionID        string                                `json:"session_id"`
	ProjectID        string                                `json:"project_id"`
	ProjectMountMode string                                `json:"project_mount_mode"`
	Project          localDockerAgentYAMLProject           `json:"project,omitempty"`
	LLM              localDockerAgentYAMLLLM               `json:"llm,omitempty"`
	Startup          localDockerAgentYAMLStartup           `json:"startup,omitempty"`
	Tools            localDockerAgentYAMLTools             `json:"tools,omitempty"`
	Environment      map[string]string                     `json:"environment,omitempty"`
	Credentials      map[string]localDockerAgentCredential `json:"credentials,omitempty"`
	Networking       localDockerAgentYAMLNetworking        `json:"networking,omitempty"`
	Directories      localDockerAgentYAMLDirectories       `json:"directories,omitempty"`
	Resources        localDockerAgentYAMLResources         `json:"resources,omitempty"`
	Labels           map[string]string                     `json:"labels,omitempty"`
	ConfigBaseDir    string                                `json:"-"`
	DefinitionDir    string                                `json:"-"`
}

type localDockerAgentCreateResult struct {
	Agent   *LocalDockerAgent
	Name    string
	Warning string
}

type localDockerAgentStartupResult struct {
	SessionID string `json:"session_id,omitempty"`
	Status    string `json:"status,omitempty"`
	AutoRun   bool   `json:"auto_run"`
	Error     string `json:"error,omitempty"`
}

type buildLocalDockerAgentImageRequest struct {
	Image   string `json:"image"`
	NoCache bool   `json:"no_cache"`
}

type localDockerAgentImageBuildResult struct {
	Status     string `json:"status"`
	Image      string `json:"image"`
	Dockerfile string `json:"dockerfile"`
	ContextDir string `json:"context_dir"`
	Output     string `json:"output"`
}

type removeLocalDockerAgentRequest struct {
	Force bool `json:"force"`
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

	autoHostPort := req.HostPort <= 0
	hostPort := req.HostPort
	portReleases := []func(){}
	defer func() {
		for i := len(portReleases) - 1; i >= 0; i-- {
			portReleases[i]()
		}
	}()
	var err error
	if autoHostPort {
		var releaseHostPort func()
		hostPort, releaseHostPort, err = reserveAvailableLocalDockerPort(ctx, defaultLocalAgentBasePort, defaultLocalAgentMaxPort)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("no available host port found in local agent range")
		}
		portReleases = append(portReleases, releaseHostPort)
	}
	if hostPort < 1 || hostPort > 65535 {
		return nil, http.StatusBadRequest, fmt.Errorf("host_port must be between 1 and 65535")
	}
	if !autoHostPort {
		var reserved bool
		var releaseHostPort func()
		releaseHostPort, reserved = reserveLocalDockerPort(hostPort)
		if !reserved {
			return nil, http.StatusBadRequest, fmt.Errorf("host_port %d is already being assigned to another local agent", hostPort)
		}
		portReleases = append(portReleases, releaseHostPort)
	}

	lmStudioBaseURL := strings.TrimSpace(req.LMStudioBaseURL)
	if lmStudioBaseURL == "" {
		lmStudioBaseURL = strings.TrimSpace(req.LLM.LMStudioBaseURL)
	}
	if lmStudioBaseURL == "" {
		lmStudioBaseURL = strings.TrimSpace(os.Getenv("LM_STUDIO_BASE_URL"))
	}
	useParentLLMProxy := s.llmProxyEnabled() && !localDockerAgentBypassesParentLLMProxy(req)
	if lmStudioBaseURL == "" && !useParentLLMProxy {
		lmStudioBaseURL = "http://host.docker.internal:1234/v1"
	}
	if useParentLLMProxy {
		lmStudioBaseURL = fmt.Sprintf("http://host.docker.internal:%d/v1", s.port)
	}
	agentKind := strings.TrimSpace(req.AgentKind)
	agentKindLabel := sanitizeDockerLabelValue(agentKind)
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	sessionID := strings.TrimSpace(req.SessionID)
	sessionIDLabel := sanitizeDockerLabelValue(sessionID)
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(req.Project.ID)
	}
	rawProjectMountMode := strings.TrimSpace(req.ProjectMountMode)
	if rawProjectMountMode == "" {
		rawProjectMountMode = strings.TrimSpace(req.Project.Mount)
	}
	projectMountMode := strings.ToLower(rawProjectMountMode)
	if projectMountMode == "" {
		projectMountMode = "ro"
	}
	if projectMountMode != "ro" && projectMountMode != "rw" {
		return nil, http.StatusBadRequest, fmt.Errorf("project_mount_mode must be either ro or rw")
	}
	if projectID == "" && rawProjectMountMode != "" {
		return nil, http.StatusBadRequest, fmt.Errorf("project_id is required when project_mount_mode is set")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to resolve home directory")
	}
	dataDir := strings.TrimSpace(req.Directories.Data)
	if dataDir == "" {
		dataDir = filepath.Join(home, ".a2gent-data", "local-agents", name)
	}
	dataDir = expandHomePath(dataDir)
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(home, dataDir)
	}
	dataDir = filepath.Clean(dataDir)
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
	proxyBaseURL := fmt.Sprintf("http://host.docker.internal:%d/v1", s.port)
	buildRunArgs := func(publishedPort int) ([]string, error) {
		args := []string{
			"run", "-d",
			"--name", name,
			"--label", localAgentManagerLabelKey + "=" + localAgentManagerLabelValue,
			"--label", "a2gent.role=brute",
			"--publish", fmt.Sprintf("%d:8080", publishedPort),
			"--volume", dataDir + ":/data",
			"--env", "HOME=/data",
			"--env", "AAGENT_DATA_PATH=/data",
			"--env", "LM_STUDIO_BASE_URL=" + lmStudioBaseURL,
		}
		var err error
		args, err = s.applyLocalDockerAgentToolsArgs(args, req.Tools)
		if err != nil {
			return nil, err
		}
		args, err = appendLocalDockerAgentExtraArgs(args, req, req.ConfigBaseDir)
		if err != nil {
			return nil, err
		}
		args, err = appendLocalDockerAgentDefinitionDirArgs(args, req.DefinitionDir, req.Name)
		if err != nil {
			return nil, err
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
		if useParentLLMProxy {
			// Route child agent traffic through the parent's OpenAI-compatible proxy.
			provider := localDockerAgentRuntimeProvider(req.LLM.Provider)
			if provider == "" {
				provider = "lmstudio"
			}
			args = append(args,
				"--env", "AAGENT_PROVIDER="+provider,
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
		return append(args, image, "server", "--port", "8080"), nil
	}

	maxAttempts := 1
	if autoHostPort {
		maxAttempts = 5
	}
	for attempt := 1; ; attempt++ {
		runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		args, buildErr := buildRunArgs(hostPort)
		if buildErr != nil {
			cancel()
			return nil, http.StatusBadRequest, buildErr
		}
		_, err = runCommand(runCtx, "docker", args...)
		cancel()
		if err == nil {
			break
		}
		if !autoHostPort || !isDockerPortAllocationError(err) || attempt >= maxAttempts {
			return nil, http.StatusBadRequest, fmt.Errorf("failed to start local agent container: %w", err)
		}
		logging.Warn("Local Docker agent: auto-selected host port %d was rejected by Docker; retrying with another port", hostPort)
		removeDockerContainerByName(ctx, name)
		var releaseHostPort func()
		hostPort, releaseHostPort, err = reserveAvailableLocalDockerPort(ctx, defaultLocalAgentBasePort, defaultLocalAgentMaxPort)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("no available host port found in local agent range")
		}
		portReleases = append(portReleases, releaseHostPort)
	}

	agent, err := findLocalBruteContainer(ctx, name)
	if err != nil {
		return &localDockerAgentCreateResult{
			Name:    name,
			Warning: "Container started but details could not be loaded: " + err.Error(),
		}, http.StatusCreated, nil
	}
	if startup := s.bootstrapLocalDockerAgentStartup(ctx, agent, req); startup != nil {
		agent.StartupSession = startup
	}
	return &localDockerAgentCreateResult{Agent: agent}, http.StatusCreated, nil
}

func (s *Server) handleBuildLocalDockerAgentImage(w http.ResponseWriter, r *http.Request) {
	var req buildLocalDockerAgentImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	result, err := buildLocalDockerAgentImage(ctx, req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to build local agent image:\n"+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, result)
}

func buildLocalDockerAgentImage(ctx context.Context, req buildLocalDockerAgentImageRequest) (localDockerAgentImageBuildResult, error) {
	image := strings.TrimSpace(req.Image)
	if image == "" {
		image = strings.TrimSpace(os.Getenv("A2GENT_LOCAL_AGENT_IMAGE"))
	}
	if image == "" {
		image = defaultLocalAgentImage
	}

	dockerfilePath, contextDir, err := resolveLocalAgentDockerBuildPaths()
	if err != nil {
		return localDockerAgentImageBuildResult{}, fmt.Errorf("failed to resolve Docker build configuration: %w", err)
	}

	args := []string{"build", "--tag", image, "--file", dockerfilePath}
	if req.NoCache {
		args = append(args, "--no-cache")
	}
	args = append(args, contextDir)

	buildOutput, err := runCommand(ctx, "docker", args...)
	if err != nil {
		return localDockerAgentImageBuildResult{}, err
	}

	return localDockerAgentImageBuildResult{
		Status:     "built",
		Image:      image,
		Dockerfile: dockerfilePath,
		ContextDir: contextDir,
		Output:     buildOutput,
	}, nil
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
