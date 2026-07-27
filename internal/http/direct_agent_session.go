package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/session"
)

func (s *Server) hasDirectAgentTarget(sess *session.Session) bool {
	if sess == nil || sess.Metadata == nil {
		return false
	}
	return strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"])) != "" || strings.TrimSpace(metadataString(sess.Metadata["docker_agent_id"])) != ""
}

func (s *Server) unifiedAgentDefinitionForSession(sess *session.Session) (*agentdef.Definition, error) {
	if sess == nil || sess.Metadata == nil {
		return nil, fmt.Errorf("session has no direct agent target")
	}
	agentID := strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"]))
	if agentID == "" {
		return nil, fmt.Errorf("session has no unified agent target")
	}
	currentProjectID := ""
	if sess.ProjectID != nil {
		currentProjectID = strings.TrimSpace(*sess.ProjectID)
	}
	def, _, err := s.definitionForUnifiedAgent(agentID, currentProjectID)
	if err != nil {
		return nil, fmt.Errorf("unified agent %q not found", agentID)
	}
	return def, nil
}

func (s *Server) resolveDirectAgentContainer(ctx context.Context, sess *session.Session) (*LocalDockerAgent, *dockerWorkspaceBinding, error) {
	if sess == nil || sess.Metadata == nil {
		return nil, nil, fmt.Errorf("session has no direct agent target")
	}
	currentProjectID := ""
	if sess.ProjectID != nil {
		currentProjectID = strings.TrimSpace(*sess.ProjectID)
	}
	if agentID := strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"])); agentID != "" {
		def, err := s.unifiedAgentDefinitionForSession(sess)
		if err != nil {
			return nil, nil, err
		}
		workspace, err := s.resolveDockerWorkspaceBinding(def, currentProjectID)
		if err != nil {
			return nil, nil, err
		}
		agent, err := s.dockerRuntime.ensureAgentContainerForWorkspace(ctx, def, workspace)
		if err != nil {
			return nil, nil, err
		}
		return agent, &workspace, nil
	}
	dockerID := strings.TrimSpace(metadataString(sess.Metadata["docker_agent_id"]))
	if dockerID == "" {
		return nil, nil, fmt.Errorf("session has no direct agent target")
	}
	agent, err := findLocalBruteContainer(ctx, dockerID)
	if err != nil {
		return nil, nil, err
	}
	return agent, nil, nil
}

func (s *Server) runDirectAgentSession(ctx context.Context, sess *session.Session, task string, onEvent func(ChatStreamEvent)) (ChatResponse, error) {
	agent, workspace, err := s.resolveDirectAgentContainer(ctx, sess)
	if err != nil {
		return ChatResponse{}, err
	}
	if !agent.Running {
		startCtx, cancel := context.WithTimeout(ctx, dockerDelegationStartTimeout)
		_, err := runCommand(startCtx, "docker", "start", agent.ID)
		cancel()
		if err != nil {
			return ChatResponse{}, fmt.Errorf("failed to start docker agent: %w", err)
		}
		agent, err = findLocalBruteContainer(ctx, agent.ID)
		if err != nil {
			return ChatResponse{}, err
		}
	}
	if agent.HostPort == 0 || strings.TrimSpace(agent.APIURL) == "" {
		return ChatResponse{}, fmt.Errorf("docker agent does not expose Brute API")
	}
	client := &nethttp.Client{}
	healthCtx, healthCancel := context.WithTimeout(ctx, dockerDelegationStartTimeout)
	err = waitForLocalDockerAgentHTTP(healthCtx, client, agent.APIURL)
	healthCancel()
	if err != nil {
		return ChatResponse{}, err
	}
	baseURL := strings.TrimRight(agent.APIURL, "/")
	createMetadata := map[string]interface{}{"source": "direct_agent_session", "parent_session_id": sess.ID}
	if workspace != nil {
		createMetadata["docker_workspace"] = dockerWorkspaceMetadata(*workspace)
	}
	var created CreateSessionResponse
	if err := postLocalDockerAgentJSON(ctx, client, baseURL+"/sessions", CreateSessionRequest{AgentID: "build", Metadata: createMetadata}, &created); err != nil {
		return ChatResponse{}, err
	}
	s.recordDockerDelegationChildSession(sess.ID, agent, created.ID, workspace)
	prompt := task
	if workspace != nil {
		prompt = s.rewriteDockerDelegationTask(task, *workspace)
	}
	return postLocalDockerAgentChatStream(ctx, client, baseURL+"/sessions/"+created.ID+"/chat/stream", ChatRequest{Message: prompt}, onEvent)
}
