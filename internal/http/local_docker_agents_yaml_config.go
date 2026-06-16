package http

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

func parseLocalDockerAgentYAMLConfig(raw []byte) (*localDockerAgentYAMLConfig, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("YAML config is empty")
	}
	var cfg localDockerAgentYAMLConfig
	if err := yaml.UnmarshalStrict(raw, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse local agent YAML: %w", err)
	}
	version := strings.TrimSpace(cfg.Version)
	if version != "" && version != "1" && version != "1.0" {
		return nil, fmt.Errorf("unsupported local agent YAML version %q", cfg.Version)
	}
	return &cfg, nil
}

func (cfg *localDockerAgentYAMLConfig) expandAgents() ([]localDockerAgentYAMLSpec, error) {
	if cfg == nil {
		return nil, fmt.Errorf("YAML config is empty")
	}

	rawAgents := make([]localDockerAgentYAMLSpec, 0, len(cfg.Agents)+1)
	if cfg.Agent != nil {
		rawAgents = append(rawAgents, *cfg.Agent)
	}
	rawAgents = append(rawAgents, cfg.Agents...)
	if len(rawAgents) == 0 {
		return nil, fmt.Errorf("YAML config must define agent or agents")
	}
	if len(rawAgents) > localDockerAgentYAMLMaxAgents {
		return nil, fmt.Errorf("agents list is too large (max %d)", localDockerAgentYAMLMaxAgents)
	}

	expanded := make([]localDockerAgentYAMLSpec, 0, len(rawAgents))
	timestampSuffix := time.Now().UnixMilli()
	for i, rawAgent := range rawAgents {
		agent := mergeLocalDockerAgentYAMLSpec(cfg.Defaults, rawAgent)
		if agent.HostPort <= 0 && cfg.Defaults.StartPort > 0 {
			agent.HostPort = cfg.Defaults.StartPort + i
		}
		if strings.TrimSpace(agent.Name) == "" {
			prefix := strings.TrimSpace(agent.NamePrefix)
			if prefix == "" {
				prefix = strings.TrimSpace(cfg.Defaults.NamePrefix)
			}
			if prefix == "" {
				prefix = localDockerAgentYAMLDefaultNamePrefix
			}
			fallbackToken := slugifyForDockerName(strings.TrimSpace(agent.AgentKind))
			if fallbackToken == "" {
				fallbackToken = fmt.Sprintf("agent-%d", i+1)
			}
			agent.Name = fmt.Sprintf("%s-%d-%d-%s", prefix, timestampSuffix, i+1, fallbackToken)
		}
		if err := validateExpandedLocalDockerAgentYAMLSpec(agent); err != nil {
			return nil, fmt.Errorf("agent %d (%s): %w", i+1, strings.TrimSpace(agent.Name), err)
		}
		expanded = append(expanded, agent)
	}
	return expanded, nil
}

func validateExpandedLocalDockerAgentYAMLSpec(spec localDockerAgentYAMLSpec) error {
	if spec.HostPort < 0 || spec.HostPort > 65535 {
		return fmt.Errorf("host_port must be between 1 and 65535 when provided")
	}
	if strings.TrimSpace(spec.ProjectID) != "" && strings.TrimSpace(spec.Project.ID) != "" && strings.TrimSpace(spec.ProjectID) != strings.TrimSpace(spec.Project.ID) {
		return fmt.Errorf("project_id and project.id disagree")
	}
	if strings.TrimSpace(spec.ProjectMountMode) != "" && strings.TrimSpace(spec.Project.Mount) != "" && !strings.EqualFold(strings.TrimSpace(spec.ProjectMountMode), strings.TrimSpace(spec.Project.Mount)) {
		return fmt.Errorf("project_mount_mode and project.mount disagree")
	}
	if (strings.TrimSpace(spec.ProjectID) != "" || strings.TrimSpace(spec.Project.ID) != "") && strings.TrimSpace(spec.Directories.Workspace.HostPath) != "" {
		return fmt.Errorf("use either project/project_id or directories.workspace, not both")
	}
	if strings.TrimSpace(spec.InitialPrompt) != "" && strings.TrimSpace(spec.Startup.Prompt) != "" && strings.TrimSpace(spec.InitialPrompt) != strings.TrimSpace(spec.Startup.Prompt) {
		return fmt.Errorf("initial_prompt and startup.prompt disagree")
	}
	return nil
}

func readLocalDockerAgentYAMLConfigFile(path string, baseDir string) ([]byte, string, error) {
	resolved := strings.TrimSpace(path)
	if resolved == "" {
		return nil, "", fmt.Errorf("config_path is required")
	}
	resolved = expandHomePath(resolved)
	if !filepath.IsAbs(resolved) {
		if strings.TrimSpace(baseDir) == "" {
			baseDir = "."
		}
		resolved = filepath.Join(baseDir, resolved)
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, resolved, fmt.Errorf("failed to access YAML config: %w", err)
	}
	if info.IsDir() {
		return nil, resolved, fmt.Errorf("YAML config path is a directory")
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, resolved, fmt.Errorf("failed to read YAML config: %w", err)
	}
	return raw, resolved, nil
}

func expandHomePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			if trimmed == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(trimmed, "~/"))
		}
	}
	return trimmed
}
func resolveLocalAgentHostPath(path string, baseDir string) string {
	resolved := expandHomePath(strings.TrimSpace(path))
	if resolved == "" {
		return ""
	}
	if filepath.IsAbs(resolved) {
		return filepath.Clean(resolved)
	}
	if strings.TrimSpace(baseDir) == "" {
		baseDir = "."
	}
	return filepath.Clean(filepath.Join(baseDir, resolved))
}
