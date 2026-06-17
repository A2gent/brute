// agent_dispatch_tool.go implements the unified delegate_to_agent tool. Local
// configured agents run through Docker with warm-container reuse; remote agents
// stay on the A2A flow.
package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const (
	dockerDelegationStartTimeout       = 30 * time.Second
	defaultDockerDelegationTaskTimeout = 12 * time.Hour
	dockerDelegationTaskTimeoutEnvVar  = "A2GENT_DOCKER_DELEGATION_TIMEOUT"
	delegationResponseMaxChars         = 4000
	dockerDelegationStreamBodyLimit    = 1024 * 1024
)

type delegateToAgentTool struct {
	server *Server
}

type delegateToAgentParams struct {
	AgentID string `json:"agent_id"`
	Task    string `json:"task"`
}

func newDelegateToAgentTool(server *Server) *delegateToAgentTool {
	return &delegateToAgentTool{server: server}
}

func dockerDelegationTaskTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(dockerDelegationTaskTimeoutEnvVar))
	if raw == "" {
		return defaultDockerDelegationTaskTimeout
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		logging.Warn("Invalid %s=%q, using default %s", dockerDelegationTaskTimeoutEnvVar, raw, defaultDockerDelegationTaskTimeout)
		return defaultDockerDelegationTaskTimeout
	}
	return parsed
}

func (t *delegateToAgentTool) Name() string {
	return "delegate_to_agent"
}

func (t *delegateToAgentTool) Description() string {
	return `Delegate a task to a configured agent. Local configured agents run in isolated Docker containers with warm reuse; remote agents run through external A2A. The dispatcher resolves the agent by ID and routes the task to the right runtime.

Use this to offload well-scoped tasks and reduce context usage in your main session. Returns the agent's response and the child session ID.`
}

func (t *delegateToAgentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_id": map[string]interface{}{
				"type":        "string",
				"description": "ID of the agent to delegate to: a configured agent ID, or a local Docker agent container name/ID. Use the list available in your system prompt or ask the user.",
			},
			"task": map[string]interface{}{
				"type":        "string",
				"description": "Clear, specific task description for the agent to complete.",
			},
		},
		"required": []string{"agent_id", "task"},
	}
}

func (t *delegateToAgentTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p delegateToAgentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	agentID := strings.TrimSpace(p.AgentID)
	task := strings.TrimSpace(p.Task)
	if agentID == "" {
		return &tools.Result{Success: false, Error: "agent_id is required"}, nil
	}
	if task == "" {
		return &tools.Result{Success: false, Error: "task is required"}, nil
	}

	// Legacy sub-agent rows are rich configuration records; execution is Docker.
	if sa, err := t.server.store.GetSubAgent(agentID); err == nil {
		return t.server.runSubAgentDockerDelegation(ctx, sa, task)
	}

	// Stored unified definitions: docker agents run in warm managed containers.
	if record, err := t.server.store.GetAgentDefinition(agentID); err == nil && record != nil {
		return t.server.runStoredDefinitionDelegation(ctx, record, task)
	}

	// Remote runtime: favorited A2A registry agents, matched by ID or name.
	if favorite := t.server.findFavoriteExternalAgent(agentID); favorite != nil {
		externalParams, err := json.Marshal(delegateToExternalAgentParams{
			TargetAgentID:   favorite.ID,
			TargetAgentName: favorite.Name,
			Task:            task,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to encode external delegation parameters: %w", err)
		}
		return newDelegateToExternalAgentTool(t.server).Execute(ctx, externalParams)
	}

	// Docker runtime: local Brute containers addressed by name or container ID.
	if dockerAgent, err := findLocalBruteContainer(ctx, agentID); err == nil {
		return t.server.runDockerAgentDelegation(ctx, dockerAgent, task)
	}

	return &tools.Result{
		Success: false,
		Error:   fmt.Sprintf("agent %q not found among configured agents, favorite external agents, or local Docker agents", agentID),
	}, nil
}

// findFavoriteExternalAgent matches a favorited A2A registry agent by ID or
// (case-insensitive) name so remote agents resolve through the same dispatcher.
func (s *Server) findFavoriteExternalAgent(agentID string) *favoriteA2AAgent {
	settings, err := s.store.GetSettings()
	if err != nil {
		return nil
	}
	favorites := parseFavoriteA2AAgents(settings[a2aFavoriteAgentsSettingKey])
	for i := range favorites {
		if favorites[i].ID == agentID || strings.EqualFold(strings.TrimSpace(favorites[i].Name), agentID) {
			return &favorites[i]
		}
	}
	return nil
}

// runStoredDefinitionDelegation routes a delegation to a stored unified agent
// definition. Docker definitions get a warm managed container with the
// workspace policy resolved against the parent session's project.
func (s *Server) runStoredDefinitionDelegation(ctx context.Context, record *storage.AgentDefinitionRecord, task string) (*tools.Result, error) {
	def, err := agentdef.ParseYAML([]byte(record.DefinitionYAML))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("stored agent definition %q is invalid: %s", record.ID, err.Error())}, nil
	}
	return s.runDockerDefinitionDelegation(ctx, record.ID, def, task)
}

func (s *Server) runSubAgentDockerDelegation(ctx context.Context, sa *storage.SubAgent, task string) (*tools.Result, error) {
	def, err := agentdef.FromSubAgent(sa)
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to build docker agent definition from saved agent: " + err.Error()}, nil
	}
	return s.runDockerDefinitionDelegation(ctx, sa.ID, def, task)
}

func (s *Server) runDockerDefinitionDelegation(ctx context.Context, agentID string, def *agentdef.Definition, task string) (*tools.Result, error) {
	if def.Runtime.Type != agentdef.RuntimeDocker {
		return &tools.Result{Success: false, Error: fmt.Sprintf("agent %q has runtime %q; local configured agents must use docker runtime", agentID, def.Runtime.Type)}, nil
	}
	currentProjectID := ""
	if parentSessionID, _ := ctx.Value("session_id").(string); parentSessionID != "" {
		if parentSess, sessErr := s.sessionManager.Get(parentSessionID); sessErr == nil && parentSess.ProjectID != nil {
			currentProjectID = strings.TrimSpace(*parentSess.ProjectID)
		}
	}

	workspace, err := s.resolveDockerWorkspaceBinding(def, currentProjectID)
	if err != nil {
		return &tools.Result{
			Success: false,
			Error:   "failed to prepare docker agent: " + err.Error(),
			Metadata: map[string]interface{}{
				"agent_id":      agentID,
				"agent_runtime": agentdef.RuntimeDocker,
			},
		}, nil
	}
	agent, err := s.dockerRuntime.ensureAgentContainerForWorkspace(ctx, def, workspace)
	if err != nil {
		return &tools.Result{
			Success: false,
			Error:   "failed to prepare docker agent: " + err.Error(),
			Metadata: map[string]interface{}{
				"agent_id":      agentID,
				"agent_runtime": agentdef.RuntimeDocker,
			},
		}, nil
	}
	return s.runDockerAgentDelegationWithWorkspace(ctx, agent, s.rewriteDockerDelegationTask(task, workspace), &workspace)
}

// runDockerAgentDelegation forwards a task to a local Docker Brute agent. The
// container is reused warm: a stopped container is started and health-checked,
// but `docker run` never happens per delegation.
func (s *Server) runDockerAgentDelegation(ctx context.Context, agent *LocalDockerAgent, task string) (*tools.Result, error) {
	return s.runDockerAgentDelegationWithWorkspace(ctx, agent, task, nil)
}

func (s *Server) runDockerAgentDelegationWithWorkspace(ctx context.Context, agent *LocalDockerAgent, task string, workspace *dockerWorkspaceBinding) (*tools.Result, error) {
	daErrorResult := func(errMsg string, extra ...map[string]interface{}) *tools.Result {
		metadata := map[string]interface{}{
			"agent_name":    agent.Name,
			"agent_id":      agent.ID,
			"agent_runtime": "docker",
		}
		if workspace != nil {
			metadata["docker_workspace"] = dockerWorkspaceMetadata(*workspace)
		}
		for _, items := range extra {
			for key, value := range items {
				metadata[key] = value
			}
		}
		return &tools.Result{
			Success:  false,
			Error:    errMsg,
			Metadata: metadata,
		}
	}

	if !agent.Running {
		startCtx, cancel := context.WithTimeout(ctx, dockerDelegationStartTimeout)
		_, err := runCommand(startCtx, "docker", "start", agent.ID)
		cancel()
		if err != nil {
			return daErrorResult("failed to start docker agent container: " + err.Error()), nil
		}
		refreshed, err := findLocalBruteContainer(ctx, agent.ID)
		if err != nil {
			return daErrorResult("docker agent started but could not be inspected: " + err.Error()), nil
		}
		agent = refreshed
	}
	if agent.HostPort == 0 || strings.TrimSpace(agent.APIURL) == "" {
		return daErrorResult("docker agent does not expose port 8080 on the host; recreate it with a published port"), nil
	}

	parentSessionID, _ := ctx.Value("session_id").(string)

	taskTimeout := dockerDelegationTaskTimeout()
	taskCtx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()
	client := &nethttp.Client{}

	healthCtx, healthCancel := context.WithTimeout(taskCtx, dockerDelegationStartTimeout)
	err := waitForLocalDockerAgentHTTP(healthCtx, client, agent.APIURL)
	healthCancel()
	if err != nil {
		return daErrorResult("docker agent is not healthy: " + err.Error()), nil
	}

	baseURL := strings.TrimRight(agent.APIURL, "/")
	createMetadata := map[string]interface{}{
		"source":            "delegate_to_agent",
		"parent_session_id": parentSessionID,
	}
	if workspace != nil {
		createMetadata["docker_workspace"] = dockerWorkspaceMetadata(*workspace)
	}
	createPayload := CreateSessionRequest{
		AgentID:  "build",
		Metadata: createMetadata,
	}
	var created CreateSessionResponse
	if err := postLocalDockerAgentJSON(taskCtx, client, baseURL+"/sessions", createPayload, &created); err != nil {
		return daErrorResult("failed to create session on docker agent: " + err.Error()), nil
	}

	logging.Info("Docker agent delegation started: parent=%s container=%s child=%s task=%s",
		parentSessionID, agent.Name, created.ID, truncateForLog(task, 100))
	s.recordDockerDelegationChildSession(parentSessionID, agent, created.ID, workspace)
	s.reportDockerDelegationToolProgress(ctx, agent, created.ID, workspace, "child_session_created", "Docker sub-agent session created. Waiting for output stream.", map[string]interface{}{
		"agent_api_url": agent.APIURL,
	})

	var chatResp ChatResponse
	chatResp, err = postLocalDockerAgentChatStream(taskCtx, client, baseURL+"/sessions/"+created.ID+"/chat/stream", ChatRequest{Message: task}, func(event ChatStreamEvent) {
		s.dockerRuntime.touch(agent.Name)
		s.logDockerDelegationStreamEvent(agent.Name, created.ID, event)
		s.reportDockerDelegationStreamProgress(ctx, agent, created.ID, workspace, event)
	})
	if err != nil {
		return daErrorResult(fmt.Sprintf("docker agent '%s' failed (child session %s): %s", agent.Name, created.ID, err.Error()), map[string]interface{}{
			"child_session_id":  created.ID,
			"parent_session_id": parentSessionID,
			"agent_api_url":     agent.APIURL,
			"logs_command":      fmt.Sprintf("docker logs --tail 200 %s", agent.Name),
		}), nil
	}
	// Refresh idleness after the task so the reaper measures from completion.
	s.dockerRuntime.touch(agent.Name)

	responseText := strings.TrimSpace(chatResp.Content)
	if responseText == "" {
		return daErrorResult(emptyDockerDelegationMessage(agent.Name, created.ID, chatResp), map[string]interface{}{
			"child_session_id":  created.ID,
			"parent_session_id": parentSessionID,
			"agent_api_url":     agent.APIURL,
		}), nil
	}
	if len(responseText) > delegationResponseMaxChars {
		responseText = responseText[:delegationResponseMaxChars] + "\n...(truncated)"
	}

	payload := map[string]interface{}{
		"success":           true,
		"agent_runtime":     "docker",
		"agent_name":        agent.Name,
		"child_session_id":  created.ID,
		"parent_session_id": parentSessionID,
		"agent_api_url":     agent.APIURL,
		"response":          responseText,
	}
	if workspace != nil {
		payload["docker_workspace"] = dockerWorkspaceMetadata(*workspace)
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool output: %w", err)
	}
	metadata := map[string]interface{}{
		"agent_runtime":     "docker",
		"agent_name":        agent.Name,
		"child_session_id":  created.ID,
		"parent_session_id": parentSessionID,
		"agent_api_url":     agent.APIURL,
	}
	if workspace != nil {
		metadata["docker_workspace"] = dockerWorkspaceMetadata(*workspace)
	}
	return &tools.Result{
		Success:  true,
		Output:   string(body),
		Metadata: metadata,
	}, nil
}

type dockerWorkspacePathMapping struct {
	ProjectID     string
	HostPath      string
	ContainerPath string
	Mode          string
}

func (s *Server) rewriteDockerDelegationTask(task string, workspace dockerWorkspaceBinding) string {
	task = strings.TrimSpace(task)
	if task == "" {
		return ""
	}

	mappings := s.dockerWorkspacePathMappings(workspace)
	if len(mappings) == 0 {
		return task
	}

	rewritten := task
	sort.SliceStable(mappings, func(i, j int) bool {
		return len(mappings[i].HostPath) > len(mappings[j].HostPath)
	})
	for _, mapping := range mappings {
		if mapping.HostPath == "" || mapping.ContainerPath == "" {
			continue
		}
		rewritten = strings.ReplaceAll(rewritten, mapping.HostPath, mapping.ContainerPath)
	}

	noteLines := []string{
		"Docker delegation workspace:",
		"- You are running inside a Docker container. Host filesystem paths are not available directly.",
	}
	if len(mappings) == 1 {
		mapping := mappings[0]
		noteLines = append(noteLines, fmt.Sprintf("- Use %s as the mounted project root for this task.", mapping.ContainerPath))
		noteLines = append(noteLines, fmt.Sprintf("- The parent project root has been mapped to %s (%s).", mapping.ContainerPath, firstNonEmptyLocalAgentString(mapping.Mode, "ro")))
	} else {
		noteLines = append(noteLines, "- Mounted project roots:")
		for _, mapping := range mappings {
			if strings.TrimSpace(mapping.ProjectID) == "" || strings.TrimSpace(mapping.ContainerPath) == "" {
				continue
			}
			noteLines = append(noteLines, fmt.Sprintf("  - %s: %s (%s)", mapping.ProjectID, mapping.ContainerPath, firstNonEmptyLocalAgentString(mapping.Mode, "ro")))
		}
	}
	note := strings.Join(noteLines, "\n")
	if strings.Contains(rewritten, "Docker delegation workspace:") {
		return rewritten
	}
	return note + "\n\nTask:\n" + rewritten
}

func (s *Server) dockerWorkspacePathMappings(workspace dockerWorkspaceBinding) []dockerWorkspacePathMapping {
	if len(workspace.ProjectMounts) > 0 {
		mappings := make([]dockerWorkspacePathMapping, 0, len(workspace.ProjectMounts))
		for _, mount := range workspace.ProjectMounts {
			hostPath := absoluteCleanPath(strings.TrimSpace(mount.HostPath), strings.TrimSpace(s.config.WorkDir))
			containerPath := strings.TrimSpace(mount.ContainerPath)
			if hostPath == "" || containerPath == "" {
				continue
			}
			mappings = append(mappings, dockerWorkspacePathMapping{
				ProjectID:     strings.TrimSpace(mount.ProjectID),
				HostPath:      hostPath,
				ContainerPath: containerPath,
				Mode:          firstNonEmptyLocalAgentString(strings.TrimSpace(mount.Mode), strings.TrimSpace(workspace.Mount)),
			})
		}
		return mappings
	}

	projectID := strings.TrimSpace(workspace.ProjectID)
	if projectID == "" {
		return nil
	}
	hostPath, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		logging.Warn("Docker delegation: failed to resolve workspace path for project %s: %v", projectID, err)
		return nil
	}
	hostPath = absoluteCleanPath(hostPath, strings.TrimSpace(s.config.WorkDir))
	if hostPath == "" {
		return nil
	}
	return []dockerWorkspacePathMapping{{
		ProjectID:     projectID,
		HostPath:      hostPath,
		ContainerPath: dockerRuntimeWorkspaceRoot,
		Mode:          strings.TrimSpace(workspace.Mount),
	}}
}

func dockerWorkspaceMetadata(workspace dockerWorkspaceBinding) map[string]interface{} {
	metadata := map[string]interface{}{
		"scope": strings.TrimSpace(workspace.Scope),
		"mount": strings.TrimSpace(workspace.Mount),
	}
	if projectID := strings.TrimSpace(workspace.ProjectID); projectID != "" {
		metadata["project_id"] = projectID
		metadata["container_path"] = dockerRuntimeWorkspaceRoot
	}
	if len(workspace.ProjectMounts) > 0 {
		mounts := make([]map[string]string, 0, len(workspace.ProjectMounts))
		for _, mount := range workspace.ProjectMounts {
			mounts = append(mounts, map[string]string{
				"project_id":     strings.TrimSpace(mount.ProjectID),
				"container_path": strings.TrimSpace(mount.ContainerPath),
				"mode":           strings.TrimSpace(mount.Mode),
			})
		}
		metadata["mounts"] = mounts
	}
	return metadata
}

func postLocalDockerAgentChatStream(ctx context.Context, client *nethttp.Client, url string, payload ChatRequest, onEvent func(ChatStreamEvent)) (ChatResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return ChatResponse{}, err
	}
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, dockerDelegationStreamBodyLimit))
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return ChatResponse{}, fmt.Errorf("POST %s failed: %s", url, msg)
	}

	var chatResp ChatResponse
	reader := bufio.NewReader(resp.Body)
	sawEvent := false
	lastType := ""
	for {
		line, readErr := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			sawEvent = true
			var event ChatStreamEvent
			if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
				return chatResp, fmt.Errorf("failed to decode stream event from %s: %w", url, err)
			}
			lastType = strings.TrimSpace(event.Type)
			applyDockerDelegationStreamEvent(&chatResp, event)
			if onEvent != nil {
				onEvent(event)
			}
			switch event.Type {
			case "done":
				return chatResp, nil
			case "error":
				message := strings.TrimSpace(event.Error)
				if message == "" {
					message = "docker agent stream failed"
				}
				return chatResp, errors.New(message)
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		return chatResp, fmt.Errorf("failed reading stream from %s: %w", url, readErr)
	}

	if sawEvent {
		if recovered, recoverErr := recoverLocalDockerAgentChatResponse(ctx, client, url, &chatResp); recoverErr == nil {
			if strings.TrimSpace(recovered.Content) != "" || isTerminalLocalAgentStatus(recovered.Status) {
				if strings.TrimSpace(recovered.Status) == string(session.StatusFailed) {
					message := strings.TrimSpace(recovered.Content)
					if message == "" {
						message = "docker agent failed before the stream sent a terminal event"
					}
					return recovered, errors.New(message)
				}
				return recovered, nil
			}
		}
		return chatResp, fmt.Errorf("stream from %s ended before terminal event (last event=%s status=%s)", url, firstNonEmptyLocalAgentString(lastType, "unknown"), firstNonEmptyLocalAgentString(chatResp.Status, "unknown"))
	}
	return chatResp, fmt.Errorf("stream from %s ended without events", url)
}

func recoverLocalDockerAgentChatResponse(ctx context.Context, client *nethttp.Client, streamURL string, fallback *ChatResponse) (ChatResponse, error) {
	sessionURL := strings.TrimSuffix(streamURL, "/chat/stream")
	if sessionURL == streamURL {
		return ChatResponse{}, fmt.Errorf("stream URL does not end with /chat/stream")
	}
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, sessionURL, nil)
	if err != nil {
		return ChatResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, dockerDelegationStreamBodyLimit))
		return ChatResponse{}, fmt.Errorf("GET %s failed: %s", sessionURL, firstNonEmptyLocalAgentString(strings.TrimSpace(string(body)), resp.Status))
	}
	var sess SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return ChatResponse{}, err
	}
	recovered := ChatResponse{
		Status:   sess.Status,
		Messages: sess.Messages,
	}
	if fallback != nil {
		recovered.Usage = fallback.Usage
	}
	recovered.Content = lastLocalAgentAssistantContent(sess.Messages)
	return recovered, nil
}

func lastLocalAgentAssistantContent(messages []MessageResponse) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && strings.TrimSpace(messages[i].Content) != "" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

func isTerminalLocalAgentStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case string(session.StatusCompleted), string(session.StatusFailed), string(session.StatusPaused), string(session.StatusInputRequired), string(session.StatusWaitingExternal):
		return true
	default:
		return false
	}
}

func applyDockerDelegationStreamEvent(resp *ChatResponse, event ChatStreamEvent) {
	if resp == nil {
		return
	}
	if status := strings.TrimSpace(event.Status); status != "" {
		resp.Status = status
	}
	if len(event.Messages) > 0 {
		resp.Messages = event.Messages
	}
	if event.Usage != nil {
		resp.Usage = *event.Usage
	}
	if event.Type == "done" {
		resp.Content = event.Content
	}
}

func (s *Server) logDockerDelegationStreamEvent(agentName string, childSessionID string, event ChatStreamEvent) {
	switch event.Type {
	case "status":
		logging.Info("Docker agent delegation status: container=%s child=%s status=%s", agentName, childSessionID, firstNonEmptyLocalAgentString(event.Status, "unknown"))
	case "tool_executing":
		logging.Info("Docker agent delegation tool started: container=%s child=%s step=%d tools=%s", agentName, childSessionID, event.Step, streamToolCallNames(event.ToolCalls))
	case "tool_completed":
		logging.Info("Docker agent delegation tool completed: container=%s child=%s step=%d status=%s", agentName, childSessionID, event.Step, firstNonEmptyLocalAgentString(event.Status, "unknown"))
	case "input_required":
		logging.Warn("Docker agent delegation needs input: container=%s child=%s status=%s", agentName, childSessionID, firstNonEmptyLocalAgentString(event.Status, "input_required"))
	case "error":
		logging.Warn("Docker agent delegation stream error: container=%s child=%s error=%s", agentName, childSessionID, truncateForLog(event.Error, 300))
	case "done":
		logging.Info("Docker agent delegation completed: container=%s child=%s status=%s response_chars=%d", agentName, childSessionID, firstNonEmptyLocalAgentString(event.Status, "unknown"), len(event.Content))
	}
}

func (s *Server) reportDockerDelegationStreamProgress(ctx context.Context, agent *LocalDockerAgent, childSessionID string, workspace *dockerWorkspaceBinding, event ChatStreamEvent) {
	status := strings.TrimSpace(event.Type)
	content := ""
	extra := map[string]interface{}{
		"child_event_type": status,
	}

	switch event.Type {
	case "status":
		status = "child_status"
		extra["child_status"] = strings.TrimSpace(event.Status)
		content = fmt.Sprintf("Docker sub-agent status: %s", firstNonEmptyLocalAgentString(strings.TrimSpace(event.Status), "unknown"))
	case "tool_executing":
		status = "child_tool_executing"
		toolNames := streamToolCallNames(event.ToolCalls)
		extra["child_step"] = event.Step
		extra["child_tool_calls"] = toolNames
		content = fmt.Sprintf("Docker sub-agent running tool: %s", toolNames)
	case "tool_completed":
		status = "child_tool_completed"
		extra["child_step"] = event.Step
		extra["child_status"] = strings.TrimSpace(event.Status)
		content = "Docker sub-agent tool completed."
	case "input_required":
		status = "child_input_required"
		extra["child_status"] = strings.TrimSpace(event.Status)
		content = "Docker sub-agent is waiting for input."
	case "error":
		status = "child_error"
		extra["child_error"] = strings.TrimSpace(event.Error)
		content = "Docker sub-agent stream failed: " + truncateForLog(event.Error, 300)
	case "done":
		status = "child_done"
		extra["child_status"] = strings.TrimSpace(event.Status)
		content = "Docker sub-agent completed. Preparing final response."
	default:
		return
	}

	s.reportDockerDelegationToolProgress(ctx, agent, childSessionID, workspace, status, content, extra)
}

func (s *Server) reportDockerDelegationToolProgress(ctx context.Context, agent *LocalDockerAgent, childSessionID string, workspace *dockerWorkspaceBinding, status string, content string, extra map[string]interface{}) {
	if agent == nil {
		return
	}
	metadata := map[string]interface{}{
		"agent_runtime":    "docker",
		"agent_name":       agent.Name,
		"child_session_id": childSessionID,
		"progress_pending": true,
	}
	if strings.TrimSpace(agent.APIURL) != "" {
		metadata["agent_api_url"] = agent.APIURL
	}
	if workspace != nil {
		metadata["docker_workspace"] = dockerWorkspaceMetadata(*workspace)
	}
	for key, value := range extra {
		metadata[key] = value
	}
	tools.ReportProgress(ctx, tools.ProgressEvent{
		ToolName: "delegate_to_agent",
		Status:   strings.TrimSpace(status),
		Content:  strings.TrimSpace(content),
		Metadata: metadata,
	})
}

func streamToolCallNames(calls []StreamToolCallEvent) string {
	if len(calls) == 0 {
		return "none"
	}
	names := make([]string, 0, len(calls))
	for i, call := range calls {
		if i >= 4 {
			names = append(names, "...")
			break
		}
		names = append(names, firstNonEmptyLocalAgentString(strings.TrimSpace(call.Name), "unknown"))
	}
	return strings.Join(names, ",")
}

func emptyDockerDelegationMessage(agentName string, childSessionID string, chatResp ChatResponse) string {
	status := strings.TrimSpace(chatResp.Status)
	if status == "" {
		status = "unknown"
	}
	message := fmt.Sprintf("docker agent %q returned empty response (child session %s, status=%s)", agentName, childSessionID, status)
	if len(chatResp.Messages) == 0 {
		return message
	}

	for i := len(chatResp.Messages) - 1; i >= 0; i-- {
		msg := chatResp.Messages[i]
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		content := truncateForLog(strings.TrimSpace(msg.Content), 500)
		return message + "; last non-empty message: " + content
	}

	toolCalls := 0
	toolResults := 0
	lastToolName := ""
	lastToolResultLen := 0
	lastToolErrored := false
	for _, msg := range chatResp.Messages {
		toolCalls += len(msg.ToolCalls)
		for _, result := range msg.ToolResults {
			toolResults++
			lastToolName = strings.TrimSpace(result.Name)
			lastToolResultLen = len(result.Content)
			lastToolErrored = result.IsError
		}
	}
	if toolCalls > 0 || toolResults > 0 {
		// WHAT: expose enough child-session diagnostics to identify a tool loop while
		// avoiding raw tool output, which can be very large or user-sensitive.
		return fmt.Sprintf("%s; child produced no final assistant text after %d tool call(s) and %d tool result(s); last_tool=%s last_tool_result_chars=%d last_tool_error=%t", message, toolCalls, toolResults, firstNonEmptyLocalAgentString(lastToolName, "unknown"), lastToolResultLen, lastToolErrored)
	}
	return message
}

var _ tools.Tool = (*delegateToAgentTool)(nil)
