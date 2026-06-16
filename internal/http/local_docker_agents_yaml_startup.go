package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func localDockerAgentStartupPrompt(req createLocalDockerAgentRequest) string {
	if prompt := strings.TrimSpace(req.Startup.Prompt); prompt != "" {
		return prompt
	}
	return strings.TrimSpace(req.InitialPrompt)
}

func localDockerAgentStartupAutoRun(req createLocalDockerAgentRequest) bool {
	if req.Startup.AutoRun == nil {
		return false
	}
	return *req.Startup.AutoRun
}

func (s *Server) bootstrapLocalDockerAgentStartup(ctx context.Context, agent *LocalDockerAgent, req createLocalDockerAgentRequest) *localDockerAgentStartupResult {
	prompt := localDockerAgentStartupPrompt(req)
	if agent == nil || strings.TrimSpace(agent.APIURL) == "" || prompt == "" {
		return nil
	}

	autoRun := localDockerAgentStartupAutoRun(req)
	timeout := 25 * time.Second
	if autoRun {
		timeout = 5 * time.Minute
	}
	startupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := &localDockerAgentStartupResult{AutoRun: autoRun}
	client := &http.Client{Timeout: timeout}
	if err := waitForLocalDockerAgentHTTP(startupCtx, client, agent.APIURL); err != nil {
		result.Error = err.Error()
		return result
	}

	agentID := strings.TrimSpace(req.AgentKind)
	if agentID == "" {
		agentID = "build"
	}
	provider := localDockerAgentRuntimeProvider(req.LLM.Provider)
	model := strings.TrimSpace(req.LLM.Model)
	metadata := map[string]interface{}{
		"source":       "local_docker_agent_yaml",
		"container_id": agent.ID,
		"container":    agent.Name,
	}
	if sessionID := strings.TrimSpace(req.SessionID); sessionID != "" {
		metadata["parent_session_id"] = sessionID
	}

	createPayload := CreateSessionRequest{
		AgentID:  agentID,
		Provider: provider,
		Model:    model,
		Metadata: metadata,
	}
	if !autoRun {
		createPayload.Task = prompt
		createPayload.Queued = true
	}

	var created CreateSessionResponse
	if err := postLocalDockerAgentJSON(startupCtx, client, strings.TrimRight(agent.APIURL, "/")+"/sessions", createPayload, &created); err != nil {
		result.Error = err.Error()
		return result
	}
	result.SessionID = created.ID
	result.Status = created.Status

	if !autoRun {
		return result
	}

	var chatResp ChatResponse
	err := postLocalDockerAgentJSON(startupCtx, client, strings.TrimRight(agent.APIURL, "/")+"/sessions/"+created.ID+"/chat", ChatRequest{Message: prompt}, &chatResp)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Status = chatResp.Status
	return result
}

func waitForLocalDockerAgentHTTP(ctx context.Context, client *http.Client, baseURL string) error {
	healthURL := strings.TrimRight(baseURL, "/") + "/health"
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("health returned HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("local agent did not become ready: %w", lastErr)
			}
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func postLocalDockerAgentJSON(ctx context.Context, client *http.Client, url string, payload interface{}, out interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("POST %s failed: %s", url, msg)
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("failed to decode response from %s: %w", url, err)
	}
	return nil
}
