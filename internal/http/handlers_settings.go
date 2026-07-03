// handlers_settings.go keeps settings endpoints isolated from the former monolithic server.go.
package http

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/filesearch"
	"github.com/A2gent/brute/internal/logging"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const agentNameSettingKey = "AAGENT_NAME"

const sessionsFolderSettingKey = "AAGENT_SESSIONS_FOLDER"

const repeatInitialPromptSettingKey = "AAGENT_REPEAT_INITIAL_PROMPT"

const defaultAgentName = "A2gent"

func isBranchTaskDocAppSettingKey(key string) bool {
	trimmedKey := strings.TrimSpace(key)
	return trimmedKey == projectBranchTaskDocDirectorySettingKey ||
		trimmedKey == projectBranchTaskDocModeSettingKey ||
		strings.HasPrefix(trimmedKey, legacyBranchTaskDocDirectorySettingPrefix) ||
		strings.HasPrefix(trimmedKey, legacyBranchTaskDocModeSettingPrefix)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load settings: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, settingsResponse(settings))
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Settings == nil {
		req.Settings = map[string]string{}
	}

	oldSettings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load existing settings: "+err.Error())
		return
	}

	if err := s.store.SaveSettings(req.Settings); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save settings: "+err.Error())
		return
	}

	syncSettingsToEnv(oldSettings, req.Settings)
	filesearch.SetIndexingEnabledFromSettings(req.Settings)
	folder := strings.TrimSpace(req.Settings[sessionsFolderSettingKey])
	if folder == "" {
		folder = filepath.Join(s.config.DataPath, "sessions")
	}
	s.sessionManager.SetJSONLFolder(folder)
	s.jsonResponse(w, http.StatusOK, settingsResponse(req.Settings))
}

func settingsResponse(settings map[string]string) SettingsResponse {
	out := make(map[string]string, len(settings)+1)
	for key, value := range settings {
		if isBranchTaskDocAppSettingKey(key) {
			continue
		}
		out[key] = value
	}
	if strings.TrimSpace(out[toolResultCompressionSettingKey]) == "" {
		// WHY: The UI should render the effective runtime default, not a missing key,
		// so new installs and existing users both see compression enabled by default.
		out[toolResultCompressionSettingKey] = "true"
	}
	if strings.TrimSpace(out[filesearch.IndexingEnabledSettingKey]) == "" {
		out[filesearch.IndexingEnabledSettingKey] = "false"
	}
	return SettingsResponse{
		Settings:                               out,
		DefaultSystemPrompt:                    agent.DefaultSystemPrompt(),
		DefaultSystemPromptWithoutBuiltInTools: agent.DefaultSystemPromptWithoutBuiltInTools(),
		DefaultPromptTemplates:                 defaultPromptTemplateSettings(),
	}
}

func (s *Server) handleEstimateInstructionPrompt(w http.ResponseWriter, r *http.Request) {
	var req UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	settings := req.Settings
	if settings == nil {
		loaded, err := s.store.GetSettings()
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to load settings: "+err.Error())
			return
		}
		settings = loaded
	}

	snapshot := s.composeSystemPromptSnapshotWithSettings(nil, settings)
	if snapshot == nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to compose instruction snapshot")
		return
	}

	blocks := make([]SystemPromptBlockSnapshotPayload, len(snapshot.Blocks))
	for i, block := range snapshot.Blocks {
		blocks[i] = SystemPromptBlockSnapshotPayload{
			Type:            block.Type,
			Value:           block.Value,
			Enabled:         block.Enabled,
			ResolvedContent: block.ResolvedContent,
			SourcePath:      block.SourcePath,
			Error:           block.Error,
			EstimatedTokens: block.EstimatedTokens,
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"snapshot": SystemPromptSnapshotPayload{
			BasePrompt:        snapshot.BasePrompt,
			CombinedPrompt:    snapshot.CombinedPrompt,
			BaseEstimated:     snapshot.BaseEstimated,
			CombinedEstimated: snapshot.CombinedEstimated,
			Blocks:            blocks,
		},
	})
}

func syncSettingsToEnv(previous map[string]string, next map[string]string) {
	for key := range previous {
		if _, ok := next[key]; ok {
			continue
		}
		k := strings.TrimSpace(key)
		if k == "" || isBranchTaskDocAppSettingKey(k) {
			continue
		}
		if err := os.Unsetenv(k); err != nil {
			logging.Warn("Failed to unset env var %q removed from settings: %v", k, err)
		}
	}

	for key, value := range next {
		k := strings.TrimSpace(key)
		if k == "" || isBranchTaskDocAppSettingKey(k) {
			continue
		}
		if err := os.Setenv(k, value); err != nil {
			logging.Warn("Failed to set env var %q from settings: %v", k, err)
		}
	}
}
