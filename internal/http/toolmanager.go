// toolmanager.go keeps tool-manager-specific helpers together after splitting the oversized server.go.
package http

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Server) resolveSessionWorkDir(sess *session.Session) string {
	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}

	if sess == nil || sess.ProjectID == nil {
		return defaultDir
	}

	projectID := strings.TrimSpace(*sess.ProjectID)
	if projectID == "" {
		return defaultDir
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		logging.Warn("Failed to load project for session workdir: session=%s project=%s error=%v", sess.ID, projectID, err)
		return defaultDir
	}

	if project.Folder != nil {
		candidate := strings.TrimSpace(*project.Folder)
		if candidate != "" {
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(defaultDir, candidate)
			}
			candidate = filepath.Clean(candidate)

			info, statErr := os.Stat(candidate)
			if statErr != nil || !info.IsDir() {
				logging.Warn("Skipping invalid project folder for session workdir: session=%s folder=%s", sess.ID, candidate)
				return defaultDir
			}
			return candidate
		}
	}

	return defaultDir
}

func (s *Server) ToolManagerForSession(sess *session.Session) *tools.Manager {
	return s.toolManagerForSession(sess)
}

func (s *Server) toolManagerForSession(sess *session.Session) *tools.Manager {
	workDir := s.resolveSessionWorkDir(sess)
	settings, err := s.store.GetSettings()
	if err != nil {
		settings = map[string]string{}
	}
	disabledTools := resolveDisabledToolNames(settings)

	isSubAgentSession := false
	var subAgentEnabledTools []string
	if sess != nil && sess.Metadata != nil {
		if saID, ok := sess.Metadata["sub_agent_id"].(string); ok && saID != "" {
			isSubAgentSession = true
			if sa, saErr := s.store.GetSubAgent(saID); saErr == nil && len(sa.EnabledTools) > 0 {
				subAgentEnabledTools = sa.EnabledTools
			}
		}
	}

	if isSubAgentSession {
		disabledTools = map[string]struct{}{}
	}

	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}
	if workDir == defaultDir && len(disabledTools) == 0 && len(subAgentEnabledTools) == 0 {
		return s.toolManager
	}

	var manager *tools.Manager
	if workDir == defaultDir {
		manager = s.toolManager.Clone()
	} else {
		manager = tools.NewManager(workDir)
		integrationtools.Register(manager, s.store, s.speechClips, s.sessionManager)
		s.registerServerBackedTools(manager)
	}

	for toolName := range disabledTools {
		manager.Unregister(toolName)
	}

	if len(subAgentEnabledTools) > 0 {
		allowed := make(map[string]struct{}, len(subAgentEnabledTools))
		for _, name := range subAgentEnabledTools {
			allowed[strings.TrimSpace(name)] = struct{}{}
		}

		allowed["question"] = struct{}{}
		allowed["session_task_progress"] = struct{}{}

		for _, def := range manager.GetDefinitions() {
			if _, ok := allowed[def.Name]; !ok {
				manager.Unregister(def.Name)
			}
		}
	}

	return manager
}

func (s *Server) registerServerBackedTools(manager *tools.Manager) {
	if manager == nil {
		logging.Warn("registerServerBackedTools called with nil manager")
		return
	}
	logging.Debug("Registering server-backed tools...")
	manager.Register(newRecurringJobsTool(s))
	manager.Register(newMCPManageTool(s))
	manager.Register(newDelegateToSubAgentTool(s))
	manager.Register(newDelegateToExternalAgentTool(s))
	manager.Register(newDiscoverExternalAgentsTool(s))
	manager.Register(newCreateLocalDockerAgentsBulkTool(s))
	manager.RegisterQuestionTool(s.sessionManager)
	manager.RegisterSessionTaskProgressTool(s.sessionManager)
	manager.RegisterSQLQueryTool(s.store)
	logging.Debug("Server-backed tools registered. Total tools: %d", len(manager.GetDefinitions()))
}

const disabledToolsSettingKey = "A2GENT_DISABLED_TOOLS"

const disableToolsByDefaultSettingKey = "A2GENT_DISABLE_TOOLS_BY_DEFAULT"

const disableToolsByDefaultAppliedSettingKey = "A2GENT_DISABLE_TOOLS_BY_DEFAULT_APPLIED"

func (s *Server) bootstrapDisabledToolsByDefault() {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(disableToolsByDefaultSettingKey)))
	if raw == "" || raw == "0" || raw == "false" || raw == "off" || raw == "no" {
		return
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to load settings for disabled-tools bootstrap: %v", err)
		return
	}
	if settings == nil {
		settings = map[string]string{}
	}
	if strings.TrimSpace(settings[disableToolsByDefaultAppliedSettingKey]) != "" {
		return
	}

	previous := make(map[string]string, len(settings))
	for key, value := range settings {
		previous[key] = value
	}

	if strings.TrimSpace(settings[disabledToolsSettingKey]) == "" {
		defs := s.toolManager.GetDefinitions()
		names := make([]string, 0, len(defs))
		for _, def := range defs {
			name := strings.TrimSpace(def.Name)
			if name != "" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		encoded, err := json.Marshal(names)
		if err != nil {
			logging.Warn("Failed to encode disabled tools bootstrap value: %v", err)
			return
		}
		settings[disabledToolsSettingKey] = string(encoded)
	}

	settings[disableToolsByDefaultAppliedSettingKey] = time.Now().UTC().Format(time.RFC3339)
	if err := s.store.SaveSettings(settings); err != nil {
		logging.Warn("Failed to save disabled-tools bootstrap setting: %v", err)
		return
	}
	syncSettingsToEnv(previous, settings)
}

func resolveDisabledToolNames(settings map[string]string) map[string]struct{} {
	disabled := make(map[string]struct{})
	if settings == nil {
		return disabled
	}

	raw := strings.TrimSpace(settings[disabledToolsSettingKey])
	if raw == "" {
		return disabled
	}

	entries := make([]string, 0)
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		entries = strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n'
		})
	}

	for _, entry := range entries {
		name := strings.TrimSpace(entry)
		if name == "" {
			continue
		}
		disabled[name] = struct{}{}
	}
	return disabled
}

func (s *Server) handleListToolDefinitions(w http.ResponseWriter, r *http.Request) {
	defs := s.toolManager.GetDefinitions()
	resp := make([]ToolDefinitionResponse, len(defs))
	for i, d := range defs {
		resp[i] = ToolDefinitionResponse{
			Name:        d.Name,
			Description: d.Description,
		}
	}
	sort.Slice(resp, func(i, j int) bool {
		return resp[i].Name < resp[j].Name
	})
	s.jsonResponse(w, http.StatusOK, resp)
}
