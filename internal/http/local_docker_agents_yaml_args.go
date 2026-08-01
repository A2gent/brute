package http

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func appendLocalAgentVolumeArg(args []string, mount localDockerAgentYAMLVolumeMount, baseDir string) ([]string, error) {
	hostPath := resolveLocalAgentHostPath(mount.HostPath, baseDir)
	containerPath := strings.TrimSpace(mount.ContainerPath)
	if hostPath == "" && containerPath == "" {
		return args, nil
	}
	if hostPath == "" || containerPath == "" {
		return nil, fmt.Errorf("volume mounts require host_path and container_path")
	}
	mode := strings.ToLower(strings.TrimSpace(mount.Mode))
	if mode == "" {
		mode = "ro"
	}
	if mode != "ro" && mode != "rw" {
		return nil, fmt.Errorf("volume mount mode for %s must be ro or rw", containerPath)
	}
	volume := hostPath + ":" + containerPath
	if mode == "ro" {
		volume += ":ro"
	}
	return append(args, "--volume", volume), nil
}

func appendLocalDockerAgentDefinitionDirArgs(args []string, definitionDir string, agentID string) ([]string, error) {
	definitionDir = resolveLocalAgentHostPath(definitionDir, "")
	if definitionDir == "" {
		return args, nil
	}
	info, err := os.Stat(definitionDir)
	if err != nil {
		return nil, fmt.Errorf("agent definition directory is not accessible: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agent definition directory must be a folder")
	}
	agentSlug := slugifyForDockerName(agentID)
	if agentSlug == "" {
		agentSlug = "agent"
	}
	containerDir := "/soul/agents/" + agentSlug
	args = append(args,
		"--volume", definitionDir+":"+containerDir+":ro",
		"--env", "A2GENT_AGENT_DEFINITION_DIR="+containerDir,
		"--env", "AAGENT_AGENT_DEFINITION_DIR="+containerDir,
	)
	if skillsDir := filepath.Join(definitionDir, "skills"); directoryExists(skillsDir) {
		// WHY: folder-based definitions keep per-agent skills beside YAML in Soul.
		// Point the child Brute at the mounted skills directory so prompt composition
		// can load the agent-specific markdown without rebuilding the Docker image.
		args = append(args, "--env", skillsFolderSettingKey+"="+containerDir+"/skills")
	}
	return args, nil
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func localDockerAgentCredentialValue(name string, credential localDockerAgentCredential, baseDir string) (string, error) {
	if strings.TrimSpace(credential.Value) != "" {
		return credential.Value, nil
	}
	if envName := strings.TrimSpace(credential.Env); envName != "" {
		value := os.Getenv(envName)
		if value == "" {
			return "", fmt.Errorf("credential %s references unset environment variable %s", name, envName)
		}
		return value, nil
	}
	if filePath := strings.TrimSpace(credential.File); filePath != "" {
		resolved := resolveLocalAgentHostPath(filePath, baseDir)
		raw, err := os.ReadFile(resolved)
		if err != nil {
			return "", fmt.Errorf("failed to read credential %s file: %w", name, err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return "", nil
}

func appendLocalDockerAgentExtraArgs(args []string, req createLocalDockerAgentRequest, baseDir string) ([]string, error) {
	req = normalizeLocalDockerAgentDockerModelRunnerRequest(req)
	if internet := req.Networking.InternetAccess; internet != nil && !*internet {
		args = append(args, "--network", "none")
	} else if network := strings.TrimSpace(req.Networking.Network); network != "" {
		args = append(args, "--network", network)
	}
	for _, alias := range req.Networking.Aliases {
		alias = strings.TrimSpace(alias)
		if alias != "" {
			args = append(args, "--network-alias", alias)
		}
	}
	for _, extraHost := range req.Networking.ExtraHosts {
		extraHost = strings.TrimSpace(extraHost)
		if extraHost != "" {
			args = append(args, "--add-host", extraHost)
		}
	}
	for _, publish := range req.Networking.Publish {
		if publish.HostPort <= 0 || publish.ContainerPort <= 0 {
			return nil, fmt.Errorf("networking.publish entries require positive host_port and container_port")
		}
		protocol := strings.ToLower(strings.TrimSpace(publish.Protocol))
		mapping := fmt.Sprintf("%d:%d", publish.HostPort, publish.ContainerPort)
		if protocol != "" {
			if protocol != "tcp" && protocol != "udp" {
				return nil, fmt.Errorf("networking.publish protocol must be tcp or udp")
			}
			mapping += "/" + protocol
		}
		args = append(args, "--publish", mapping)
	}

	for key, value := range req.Labels {
		labelKey := strings.TrimSpace(key)
		if labelKey != "" {
			args = append(args, "--label", labelKey+"="+sanitizeDockerLabelValue(value))
		}
	}

	for key, value := range req.Environment {
		envKey := strings.TrimSpace(key)
		if envKey != "" {
			args = append(args, "--env", envKey+"="+value)
		}
	}
	for key, credential := range req.Credentials {
		envKey := strings.TrimSpace(key)
		if envKey == "" {
			continue
		}
		value, err := localDockerAgentCredentialValue(envKey, credential, baseDir)
		if err != nil {
			return nil, err
		}
		args = append(args, "--env", envKey+"="+value)
	}
	if provider := localDockerAgentRuntimeProvider(req.LLM.Provider); provider != "" {
		args = append(args, "--env", "AAGENT_PROVIDER="+provider)
	}
	if model := strings.TrimSpace(req.LLM.Model); model != "" {
		args = append(args, "--env", "AAGENT_MODEL="+model)
	}
	if effort := strings.TrimSpace(req.LLM.ReasoningEffort); effort != "" {
		args = append(args, "--env", "AAGENT_OPENAI_CODEX_REASONING_EFFORT="+effort)
	}
	args = appendLocalDockerAgentBaseURLEnv(args, req)

	if workspace := req.Directories.Workspace; strings.TrimSpace(workspace.HostPath) != "" || strings.TrimSpace(workspace.ContainerPath) != "" {
		var err error
		args, err = appendLocalAgentVolumeArg(args, workspace, baseDir)
		if err != nil {
			return nil, err
		}
	}
	for _, volume := range req.Directories.Volumes {
		var err error
		args, err = appendLocalAgentVolumeArg(args, volume, baseDir)
		if err != nil {
			return nil, err
		}
	}

	if cpus := strings.TrimSpace(req.Resources.CPUs); cpus != "" {
		args = append(args, "--cpus", cpus)
	}
	if memory := strings.TrimSpace(req.Resources.Memory); memory != "" {
		args = append(args, "--memory", memory)
	}
	if gpus := strings.TrimSpace(req.Resources.GPUs); gpus != "" {
		args = append(args, "--gpus", gpus)
	}
	return args, nil
}

func normalizeLocalDockerAgentDockerModelRunnerRequest(req createLocalDockerAgentRequest) createLocalDockerAgentRequest {
	if !isDockerModelRunnerProvider(req.LLM.Provider) {
		return req
	}
	if strings.TrimSpace(req.LLM.BaseURL) == "" {
		req.LLM.BaseURL = dockerModelRunnerOpenAIBaseURL
	}
	if req.Environment == nil {
		req.Environment = map[string]string{}
	}
	if _, ok := req.Environment["OPENAI_API_KEY"]; !ok {
		if _, ok := req.Credentials["OPENAI_API_KEY"]; !ok {
			req.Environment["OPENAI_API_KEY"] = "docker-model-runner"
		}
	}
	if !hasExtraHost(req.Networking.ExtraHosts, "model-runner.docker.internal") {
		req.Networking.ExtraHosts = append(req.Networking.ExtraHosts, "model-runner.docker.internal:host-gateway")
	}
	return req
}

func isDockerModelRunnerProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "dmr", "docker_model_runner", "docker-model-runner", "docker_modelrunner", "docker-modelrunner":
		return true
	default:
		return false
	}
}

func localDockerAgentRuntimeProvider(provider string) string {
	trimmed := strings.TrimSpace(provider)
	if isDockerModelRunnerProvider(trimmed) {
		return "openai"
	}
	return trimmed
}

func localDockerAgentBypassesParentLLMProxy(req createLocalDockerAgentRequest) bool {
	return isDockerModelRunnerProvider(req.LLM.Provider) ||
		strings.TrimSpace(req.LLM.BaseURL) != "" ||
		strings.TrimSpace(req.LMStudioBaseURL) != "" ||
		strings.TrimSpace(req.LLM.LMStudioBaseURL) != ""
}

func appendLocalDockerAgentBaseURLEnv(args []string, req createLocalDockerAgentRequest) []string {
	baseURL := strings.TrimSpace(req.LLM.BaseURL)
	if baseURL == "" {
		return args
	}
	provider := localDockerAgentRuntimeProvider(req.LLM.Provider)
	switch strings.ToLower(provider) {
	case "openai":
		return append(args, "--env", "OPENAI_BASE_URL="+baseURL)
	case "openrouter":
		return append(args, "--env", "OPENROUTER_BASE_URL="+baseURL)
	case "google", "gemini":
		return append(args, "--env", "GOOGLE_BASE_URL="+baseURL)
	case "kimi":
		return append(args, "--env", "KIMI_BASE_URL="+baseURL)
	case "lmstudio":
		return append(args, "--env", "LM_STUDIO_BASE_URL="+baseURL)
	case "opencode_zen":
		return append(args, "--env", "OPENCODE_ZEN_BASE_URL="+baseURL)
	default:
		return args
	}
}

func hasExtraHost(values []string, host string) bool {
	host = strings.TrimSpace(host)
	for _, value := range values {
		if strings.HasPrefix(strings.TrimSpace(value), host+":") {
			return true
		}
	}
	return false
}

func (s *Server) applyLocalDockerAgentToolsArgs(args []string, toolSpec localDockerAgentYAMLTools) ([]string, error) {
	apply, disabledJSON, err := s.disabledToolsJSONForLocalAgent(toolSpec)
	if err != nil {
		return nil, err
	}
	args = append(args, "--env", syncDisabledToolsFromEnvSettingKey+"=true")
	if apply {
		args = append(args, "--env", disableToolsByDefaultSettingKey+"=true")
		if strings.TrimSpace(disabledJSON) != "" {
			args = append(args, "--env", disabledToolsSettingKey+"="+disabledJSON)
		}
		return args, nil
	}
	return append(args, "--env", disableToolsByDefaultSettingKey+"=false"), nil
}

func (s *Server) disabledToolsJSONForLocalAgent(toolSpec localDockerAgentYAMLTools) (bool, string, error) {
	mode := strings.ToLower(strings.TrimSpace(toolSpec.Mode))
	enabledSet := normalizeToolNameSet(toolSpec.Enabled)
	disabledSet := normalizeToolNameSet(toolSpec.Disabled)
	allowAll := mode == "all" || hasWildcardTool(enabledSet)

	if allowAll {
		if len(disabledSet) == 0 {
			return false, "", nil
		}
		disabled, err := json.Marshal(sortedStringSet(disabledSet))
		return true, string(disabled), err
	}

	if len(enabledSet) > 0 {
		allTools := make(map[string]struct{})
		if s != nil && s.toolManager != nil {
			for _, def := range s.toolManager.GetDefinitions() {
				name := strings.TrimSpace(def.Name)
				if name != "" {
					allTools[name] = struct{}{}
				}
			}
		}
		disabledSet = mergeToolSets(disabledSet, complementToolSet(allTools, enabledSet))
		disabled, err := json.Marshal(sortedStringSet(disabledSet))
		return true, string(disabled), err
	}

	if len(disabledSet) > 0 {
		disabled, err := json.Marshal(sortedStringSet(disabledSet))
		return true, string(disabled), err
	}

	// Preserve current safe default for local Docker agents: tools start disabled
	// unless YAML explicitly opts into all tools or an allow/block list.
	return true, "", nil
}

func normalizeToolNameSet(values []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func hasWildcardTool(values map[string]struct{}) bool {
	_, star := values["*"]
	_, all := values["all"]
	return star || all
}

func complementToolSet(allTools, enabled map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for name := range allTools {
		if _, ok := enabled[name]; ok {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func mergeToolSets(first, second map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(first)+len(second))
	for name := range first {
		out[name] = struct{}{}
	}
	for name := range second {
		out[name] = struct{}{}
	}
	return out
}

func sortedStringSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
