package http

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
)

func (s *Server) createLocalDockerAgentsFromYAML(ctx context.Context, rawYAML []byte, configPath string) (*localDockerAgentsFromYAMLResult, int, error) {
	cfg, err := parseLocalDockerAgentYAMLConfig(rawYAML)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	agents, err := cfg.expandAgents()
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	baseDir := ""
	if strings.TrimSpace(configPath) != "" {
		baseDir = filepath.Dir(configPath)
	}

	result := &localDockerAgentsFromYAMLResult{
		Requested:  len(agents),
		Created:    make([]map[string]interface{}, 0, len(agents)),
		Failures:   make([]map[string]interface{}, 0),
		ConfigPath: configPath,
	}
	for i, spec := range agents {
		createReq := spec.toCreateRequest()
		createReq.ConfigBaseDir = baseDir
		createResult, _, createErr := s.createLocalDockerAgent(ctx, createReq)
		if createErr != nil {
			result.Failures = append(result.Failures, map[string]interface{}{
				"index": i,
				"name":  createReq.Name,
				"error": createErr.Error(),
			})
			if cfg.ContinueOnError != nil && !*cfg.ContinueOnError {
				break
			}
			continue
		}
		entry := map[string]interface{}{"index": i}
		if createResult.Agent != nil {
			entry["agent"] = createResult.Agent
			if localDockerAgentYAMLRegistryEnabled(spec.Registry) {
				registerResp, _, registerErr := s.registerLocalDockerAgent(ctx, createResult.Agent, spec.toRegisterRequest())
				if registerErr != nil {
					entry["registry_error"] = registerErr.Error()
					result.Failures = append(result.Failures, map[string]interface{}{
						"index": i,
						"name":  createReq.Name,
						"error": "registry registration failed: " + registerErr.Error(),
					})
					if cfg.ContinueOnError != nil && !*cfg.ContinueOnError {
						result.Created = append(result.Created, entry)
						break
					}
				} else {
					entry["registry"] = registerResp
				}
			}
		} else {
			entry["agent"] = map[string]interface{}{
				"name":    createResult.Name,
				"status":  "started",
				"warning": createResult.Warning,
			}
		}
		result.Created = append(result.Created, entry)
	}
	result.CreatedCount = len(result.Created)
	result.FailedCount = len(result.Failures)
	result.Success = result.FailedCount == 0
	status := http.StatusCreated
	if result.CreatedCount == 0 && result.FailedCount > 0 {
		status = http.StatusBadRequest
	}
	return result, status, nil
}
