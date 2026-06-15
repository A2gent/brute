package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

const (
	localDockerAgentYAMLMaxAgents         = 100
	localDockerAgentYAMLDefaultNamePrefix = "a2gent-local"
	dockerModelRunnerOpenAIBaseURL        = "http://model-runner.docker.internal/engines/v1"
)

type createLocalDockerAgentsFromYAMLRequest struct {
	ConfigYAML string `json:"config_yaml"`
	ConfigPath string `json:"config_path"`
}

type localDockerAgentsFromYAMLResult struct {
	Success      bool                     `json:"success"`
	Requested    int                      `json:"requested"`
	CreatedCount int                      `json:"created_count"`
	FailedCount  int                      `json:"failed_count"`
	Created      []map[string]interface{} `json:"created"`
	Failures     []map[string]interface{} `json:"failures"`
	ConfigPath   string                   `json:"config_path,omitempty"`
}

type localDockerAgentYAMLConfig struct {
	Version         string                     `yaml:"version" json:"version,omitempty"`
	ContinueOnError *bool                      `yaml:"continue_on_error" json:"continue_on_error,omitempty"`
	Defaults        localDockerAgentYAMLSpec   `yaml:"defaults" json:"defaults,omitempty"`
	Agent           *localDockerAgentYAMLSpec  `yaml:"agent" json:"agent,omitempty"`
	Agents          []localDockerAgentYAMLSpec `yaml:"agents" json:"agents,omitempty"`
}

type localDockerAgentYAMLSpec struct {
	Name             string                                `yaml:"name" json:"name,omitempty"`
	Description      string                                `yaml:"description" json:"description,omitempty"`
	Emoji            string                                `yaml:"emoji" json:"emoji,omitempty"`
	IconURL          string                                `yaml:"icon_url" json:"icon_url,omitempty"`
	AvatarURL        string                                `yaml:"avatar_url" json:"avatar_url,omitempty"`
	Category         string                                `yaml:"category" json:"category,omitempty"`
	NamePrefix       string                                `yaml:"name_prefix" json:"name_prefix,omitempty"`
	StartPort        int                                   `yaml:"start_port" json:"start_port,omitempty"`
	Image            string                                `yaml:"image" json:"image,omitempty"`
	HostPort         int                                   `yaml:"host_port" json:"host_port,omitempty"`
	LMStudioBaseURL  string                                `yaml:"lm_studio_base_url" json:"lm_studio_base_url,omitempty"`
	AgentKind        string                                `yaml:"agent_kind" json:"agent_kind,omitempty"`
	SystemPrompt     string                                `yaml:"system_prompt" json:"system_prompt,omitempty"`
	InitialPrompt    string                                `yaml:"initial_prompt" json:"initial_prompt,omitempty"`
	SessionID        string                                `yaml:"session_id" json:"session_id,omitempty"`
	ProjectID        string                                `yaml:"project_id" json:"project_id,omitempty"`
	ProjectMountMode string                                `yaml:"project_mount_mode" json:"project_mount_mode,omitempty"`
	Project          localDockerAgentYAMLProject           `yaml:"project" json:"project,omitempty"`
	LLM              localDockerAgentYAMLLLM               `yaml:"llm" json:"llm,omitempty"`
	Startup          localDockerAgentYAMLStartup           `yaml:"startup" json:"startup,omitempty"`
	Tools            localDockerAgentYAMLTools             `yaml:"tools" json:"tools,omitempty"`
	Registry         localDockerAgentYAMLRegistry          `yaml:"registry" json:"registry,omitempty"`
	Environment      map[string]string                     `yaml:"environment" json:"environment,omitempty"`
	Credentials      map[string]localDockerAgentCredential `yaml:"credentials" json:"credentials,omitempty"`
	Networking       localDockerAgentYAMLNetworking        `yaml:"networking" json:"networking,omitempty"`
	Directories      localDockerAgentYAMLDirectories       `yaml:"directories" json:"directories,omitempty"`
	Resources        localDockerAgentYAMLResources         `yaml:"resources" json:"resources,omitempty"`
	Labels           map[string]string                     `yaml:"labels" json:"labels,omitempty"`
}

type localDockerAgentYAMLProject struct {
	ID    string `yaml:"id" json:"id,omitempty"`
	Mount string `yaml:"mount" json:"mount,omitempty"`
}

type localDockerAgentYAMLLLM struct {
	Provider        string `yaml:"provider" json:"provider,omitempty"`
	Model           string `yaml:"model" json:"model,omitempty"`
	BaseURL         string `yaml:"base_url" json:"base_url,omitempty"`
	LMStudioBaseURL string `yaml:"lm_studio_base_url" json:"lm_studio_base_url,omitempty"`
}

type localDockerAgentYAMLStartup struct {
	Prompt  string `yaml:"prompt" json:"prompt,omitempty"`
	AutoRun *bool  `yaml:"auto_run" json:"auto_run,omitempty"`
}

type localDockerAgentYAMLTools struct {
	Mode     string   `yaml:"mode" json:"mode,omitempty"`
	Enabled  []string `yaml:"enabled" json:"enabled,omitempty"`
	Disabled []string `yaml:"disabled" json:"disabled,omitempty"`
}
type localDockerAgentYAMLRegistry struct {
	Enabled            *bool   `yaml:"enabled" json:"enabled,omitempty"`
	RegistryURL        string  `yaml:"registry_url" json:"registry_url,omitempty"`
	OwnerEmail         string  `yaml:"owner_email" json:"owner_email,omitempty"`
	AgentName          string  `yaml:"agent_name" json:"agent_name,omitempty"`
	AgentHandle        string  `yaml:"agent_handle" json:"agent_handle,omitempty"`
	AgentID            string  `yaml:"agent_id" json:"agent_id,omitempty"`
	PublicID           string  `yaml:"public_id" json:"public_id,omitempty"`
	OrganizationHandle string  `yaml:"organization_handle" json:"organization_handle,omitempty"`
	Description        string  `yaml:"description" json:"description,omitempty"`
	NetworkAccess      string  `yaml:"network_access" json:"network_access,omitempty"`
	EndpointURL        string  `yaml:"endpoint_url" json:"endpoint_url,omitempty"`
	AgentType          string  `yaml:"agent_type" json:"agent_type,omitempty"`
	Category           string  `yaml:"category" json:"category,omitempty"`
	Discoverable       *bool   `yaml:"discoverable" json:"discoverable,omitempty"`
	OfficialWebsite    string  `yaml:"official_website" json:"official_website,omitempty"`
	AvatarPath         string  `yaml:"avatar_path" json:"avatar_path,omitempty"`
	AvatarURL          string  `yaml:"avatar_url" json:"avatar_url,omitempty"`
	SupportsAudio      *bool   `yaml:"supports_audio" json:"supports_audio,omitempty"`
	SupportsImages     *bool   `yaml:"supports_images" json:"supports_images,omitempty"`
	SupportsVideo      *bool   `yaml:"supports_video" json:"supports_video,omitempty"`
	PricingModel       string  `yaml:"pricing_model" json:"pricing_model,omitempty"`
	PricePerRequest    float64 `yaml:"price_per_request" json:"price_per_request,omitempty"`
	PricePerInputKB    float64 `yaml:"price_per_input_kb" json:"price_per_input_kb,omitempty"`
	PricePerOutputKB   float64 `yaml:"price_per_output_kb" json:"price_per_output_kb,omitempty"`
	PricePerSession    float64 `yaml:"price_per_session" json:"price_per_session,omitempty"`
	Currency           string  `yaml:"currency" json:"currency,omitempty"`
	ConfigureContainer *bool   `yaml:"configure_container" json:"configure_container,omitempty"`
	Transport          string  `yaml:"transport" json:"transport,omitempty"`
	SquareGRPCAddr     string  `yaml:"square_grpc_addr" json:"square_grpc_addr,omitempty"`
	SquareWSURL        string  `yaml:"square_ws_url" json:"square_ws_url,omitempty"`
}

type localDockerAgentCredential struct {
	Value string `yaml:"value" json:"value,omitempty"`
	Env   string `yaml:"env" json:"env,omitempty"`
	File  string `yaml:"file" json:"file,omitempty"`
}

type localDockerAgentYAMLNetworking struct {
	Network        string                            `yaml:"network" json:"network,omitempty"`
	InternetAccess *bool                             `yaml:"internet_access" json:"internet_access,omitempty"`
	Aliases        []string                          `yaml:"aliases" json:"aliases,omitempty"`
	ExtraHosts     []string                          `yaml:"extra_hosts" json:"extra_hosts,omitempty"`
	Publish        []localDockerAgentYAMLPortPublish `yaml:"publish" json:"publish,omitempty"`
}

type localDockerAgentYAMLPortPublish struct {
	HostPort      int    `yaml:"host_port" json:"host_port,omitempty"`
	ContainerPort int    `yaml:"container_port" json:"container_port,omitempty"`
	Protocol      string `yaml:"protocol" json:"protocol,omitempty"`
}

type localDockerAgentYAMLDirectories struct {
	Data      string                            `yaml:"data" json:"data,omitempty"`
	Workspace localDockerAgentYAMLVolumeMount   `yaml:"workspace" json:"workspace,omitempty"`
	Volumes   []localDockerAgentYAMLVolumeMount `yaml:"volumes" json:"volumes,omitempty"`
}

type localDockerAgentYAMLVolumeMount struct {
	HostPath      string `yaml:"host_path" json:"host_path,omitempty"`
	ContainerPath string `yaml:"container_path" json:"container_path,omitempty"`
	Mode          string `yaml:"mode" json:"mode,omitempty"`
}

type localDockerAgentYAMLResources struct {
	CPUs   string `yaml:"cpus" json:"cpus,omitempty"`
	Memory string `yaml:"memory" json:"memory,omitempty"`
	GPUs   string `yaml:"gpus" json:"gpus,omitempty"`
}

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

func mergeLocalDockerAgentYAMLSpec(base, override localDockerAgentYAMLSpec) localDockerAgentYAMLSpec {
	out := base
	if strings.TrimSpace(override.Name) != "" {
		out.Name = override.Name
	}
	if strings.TrimSpace(override.Description) != "" {
		out.Description = override.Description
	}
	if strings.TrimSpace(override.Emoji) != "" {
		out.Emoji = override.Emoji
	}
	if strings.TrimSpace(override.IconURL) != "" {
		out.IconURL = override.IconURL
	}
	if strings.TrimSpace(override.AvatarURL) != "" {
		out.AvatarURL = override.AvatarURL
	}
	if strings.TrimSpace(override.Category) != "" {
		out.Category = override.Category
	}
	if strings.TrimSpace(override.NamePrefix) != "" {
		out.NamePrefix = override.NamePrefix
	}
	if override.StartPort > 0 {
		out.StartPort = override.StartPort
	}
	if strings.TrimSpace(override.Image) != "" {
		out.Image = override.Image
	}
	if override.HostPort > 0 {
		out.HostPort = override.HostPort
	}
	if strings.TrimSpace(override.LMStudioBaseURL) != "" {
		out.LMStudioBaseURL = override.LMStudioBaseURL
	}
	if strings.TrimSpace(override.AgentKind) != "" {
		out.AgentKind = override.AgentKind
	}
	if strings.TrimSpace(override.SystemPrompt) != "" {
		out.SystemPrompt = override.SystemPrompt
	}
	if strings.TrimSpace(override.InitialPrompt) != "" {
		out.InitialPrompt = override.InitialPrompt
	}
	if strings.TrimSpace(override.SessionID) != "" {
		out.SessionID = override.SessionID
	}
	if strings.TrimSpace(override.ProjectID) != "" {
		out.ProjectID = override.ProjectID
	}
	if strings.TrimSpace(override.ProjectMountMode) != "" {
		out.ProjectMountMode = override.ProjectMountMode
	}
	out.Project = mergeLocalDockerAgentYAMLProject(out.Project, override.Project)
	out.LLM = mergeLocalDockerAgentYAMLLLM(out.LLM, override.LLM)
	out.Startup = mergeLocalDockerAgentYAMLStartup(out.Startup, override.Startup)
	out.Tools = mergeLocalDockerAgentYAMLTools(out.Tools, override.Tools)
	out.Registry = mergeLocalDockerAgentYAMLRegistry(out.Registry, override.Registry)
	out.Environment = mergeStringMap(out.Environment, override.Environment)
	out.Credentials = mergeCredentialMap(out.Credentials, override.Credentials)
	out.Networking = mergeLocalDockerAgentYAMLNetworking(out.Networking, override.Networking)
	out.Directories = mergeLocalDockerAgentYAMLDirectories(out.Directories, override.Directories)
	out.Resources = mergeLocalDockerAgentYAMLResources(out.Resources, override.Resources)
	out.Labels = mergeStringMap(out.Labels, override.Labels)
	return out
}

func mergeLocalDockerAgentYAMLProject(base, override localDockerAgentYAMLProject) localDockerAgentYAMLProject {
	out := base
	if strings.TrimSpace(override.ID) != "" {
		out.ID = override.ID
	}
	if strings.TrimSpace(override.Mount) != "" {
		out.Mount = override.Mount
	}
	return out
}

func mergeLocalDockerAgentYAMLLLM(base, override localDockerAgentYAMLLLM) localDockerAgentYAMLLLM {
	out := base
	if strings.TrimSpace(override.Provider) != "" {
		out.Provider = override.Provider
	}
	if strings.TrimSpace(override.Model) != "" {
		out.Model = override.Model
	}
	if strings.TrimSpace(override.BaseURL) != "" {
		out.BaseURL = override.BaseURL
	}
	if strings.TrimSpace(override.LMStudioBaseURL) != "" {
		out.LMStudioBaseURL = override.LMStudioBaseURL
	}
	return out
}

func mergeLocalDockerAgentYAMLStartup(base, override localDockerAgentYAMLStartup) localDockerAgentYAMLStartup {
	out := base
	if strings.TrimSpace(override.Prompt) != "" {
		out.Prompt = override.Prompt
	}
	if override.AutoRun != nil {
		v := *override.AutoRun
		out.AutoRun = &v
	}
	return out
}

func mergeLocalDockerAgentYAMLTools(base, override localDockerAgentYAMLTools) localDockerAgentYAMLTools {
	out := base
	if strings.TrimSpace(override.Mode) != "" {
		out.Mode = override.Mode
	}
	if len(override.Enabled) > 0 {
		out.Enabled = append([]string(nil), override.Enabled...)
	}
	if len(override.Disabled) > 0 {
		out.Disabled = append([]string(nil), override.Disabled...)
	}
	return out
}

func mergeLocalDockerAgentYAMLRegistry(base, override localDockerAgentYAMLRegistry) localDockerAgentYAMLRegistry {
	out := base
	if override.Enabled != nil {
		v := *override.Enabled
		out.Enabled = &v
	}
	if strings.TrimSpace(override.RegistryURL) != "" {
		out.RegistryURL = override.RegistryURL
	}
	if strings.TrimSpace(override.OwnerEmail) != "" {
		out.OwnerEmail = override.OwnerEmail
	}
	if strings.TrimSpace(override.AgentName) != "" {
		out.AgentName = override.AgentName
	}
	if strings.TrimSpace(override.AgentHandle) != "" {
		out.AgentHandle = override.AgentHandle
	}
	if strings.TrimSpace(override.AgentID) != "" {
		out.AgentID = override.AgentID
	}
	if strings.TrimSpace(override.PublicID) != "" {
		out.PublicID = override.PublicID
	}
	if strings.TrimSpace(override.OrganizationHandle) != "" {
		out.OrganizationHandle = override.OrganizationHandle
	}
	if strings.TrimSpace(override.Description) != "" {
		out.Description = override.Description
	}
	if strings.TrimSpace(override.NetworkAccess) != "" {
		out.NetworkAccess = override.NetworkAccess
	}
	if strings.TrimSpace(override.EndpointURL) != "" {
		out.EndpointURL = override.EndpointURL
	}
	if strings.TrimSpace(override.AgentType) != "" {
		out.AgentType = override.AgentType
	}
	if strings.TrimSpace(override.Category) != "" {
		out.Category = override.Category
	}
	if override.Discoverable != nil {
		v := *override.Discoverable
		out.Discoverable = &v
	}
	if strings.TrimSpace(override.OfficialWebsite) != "" {
		out.OfficialWebsite = override.OfficialWebsite
	}
	if strings.TrimSpace(override.AvatarPath) != "" {
		out.AvatarPath = override.AvatarPath
	}
	if strings.TrimSpace(override.AvatarURL) != "" {
		out.AvatarURL = override.AvatarURL
	}
	if override.SupportsAudio != nil {
		v := *override.SupportsAudio
		out.SupportsAudio = &v
	}
	if override.SupportsImages != nil {
		v := *override.SupportsImages
		out.SupportsImages = &v
	}
	if override.SupportsVideo != nil {
		v := *override.SupportsVideo
		out.SupportsVideo = &v
	}
	if strings.TrimSpace(override.PricingModel) != "" {
		out.PricingModel = override.PricingModel
	}
	if override.PricePerRequest > 0 {
		out.PricePerRequest = override.PricePerRequest
	}
	if override.PricePerInputKB > 0 {
		out.PricePerInputKB = override.PricePerInputKB
	}
	if override.PricePerOutputKB > 0 {
		out.PricePerOutputKB = override.PricePerOutputKB
	}
	if override.PricePerSession > 0 {
		out.PricePerSession = override.PricePerSession
	}
	if strings.TrimSpace(override.Currency) != "" {
		out.Currency = override.Currency
	}
	if override.ConfigureContainer != nil {
		v := *override.ConfigureContainer
		out.ConfigureContainer = &v
	}
	if strings.TrimSpace(override.Transport) != "" {
		out.Transport = override.Transport
	}
	if strings.TrimSpace(override.SquareGRPCAddr) != "" {
		out.SquareGRPCAddr = override.SquareGRPCAddr
	}
	if strings.TrimSpace(override.SquareWSURL) != "" {
		out.SquareWSURL = override.SquareWSURL
	}
	return out
}

func mergeLocalDockerAgentYAMLNetworking(base, override localDockerAgentYAMLNetworking) localDockerAgentYAMLNetworking {
	out := base
	if strings.TrimSpace(override.Network) != "" {
		out.Network = override.Network
	}
	if override.InternetAccess != nil {
		v := *override.InternetAccess
		out.InternetAccess = &v
	}
	if len(override.Aliases) > 0 {
		out.Aliases = append([]string(nil), override.Aliases...)
	}
	if len(override.ExtraHosts) > 0 {
		out.ExtraHosts = append([]string(nil), override.ExtraHosts...)
	}
	if len(override.Publish) > 0 {
		out.Publish = append([]localDockerAgentYAMLPortPublish(nil), override.Publish...)
	}
	return out
}

func mergeLocalDockerAgentYAMLDirectories(base, override localDockerAgentYAMLDirectories) localDockerAgentYAMLDirectories {
	out := base
	if strings.TrimSpace(override.Data) != "" {
		out.Data = override.Data
	}
	out.Workspace = mergeLocalDockerAgentYAMLVolumeMount(out.Workspace, override.Workspace)
	if len(override.Volumes) > 0 {
		out.Volumes = append([]localDockerAgentYAMLVolumeMount(nil), override.Volumes...)
	}
	return out
}

func mergeLocalDockerAgentYAMLVolumeMount(base, override localDockerAgentYAMLVolumeMount) localDockerAgentYAMLVolumeMount {
	out := base
	if strings.TrimSpace(override.HostPath) != "" {
		out.HostPath = override.HostPath
	}
	if strings.TrimSpace(override.ContainerPath) != "" {
		out.ContainerPath = override.ContainerPath
	}
	if strings.TrimSpace(override.Mode) != "" {
		out.Mode = override.Mode
	}
	return out
}

func mergeLocalDockerAgentYAMLResources(base, override localDockerAgentYAMLResources) localDockerAgentYAMLResources {
	out := base
	if strings.TrimSpace(override.CPUs) != "" {
		out.CPUs = override.CPUs
	}
	if strings.TrimSpace(override.Memory) != "" {
		out.Memory = override.Memory
	}
	if strings.TrimSpace(override.GPUs) != "" {
		out.GPUs = override.GPUs
	}
	return out
}

func mergeStringMap(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

func mergeCredentialMap(base, override map[string]localDockerAgentCredential) map[string]localDockerAgentCredential {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]localDockerAgentCredential, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

func (spec localDockerAgentYAMLSpec) toCreateRequest() createLocalDockerAgentRequest {
	labels := localDockerAgentYAMLMetadataLabels(spec)
	projectID := strings.TrimSpace(spec.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(spec.Project.ID)
	}
	projectMountMode := strings.TrimSpace(spec.ProjectMountMode)
	if projectMountMode == "" {
		projectMountMode = strings.TrimSpace(spec.Project.Mount)
	}
	lmStudioBaseURL := strings.TrimSpace(spec.LMStudioBaseURL)
	if lmStudioBaseURL == "" {
		lmStudioBaseURL = strings.TrimSpace(spec.LLM.LMStudioBaseURL)
	}
	return createLocalDockerAgentRequest{
		Name:             strings.TrimSpace(spec.Name),
		Image:            strings.TrimSpace(spec.Image),
		HostPort:         spec.HostPort,
		LMStudioBaseURL:  lmStudioBaseURL,
		AgentKind:        strings.TrimSpace(spec.AgentKind),
		SystemPrompt:     strings.TrimSpace(spec.SystemPrompt),
		InitialPrompt:    strings.TrimSpace(spec.InitialPrompt),
		SessionID:        strings.TrimSpace(spec.SessionID),
		ProjectID:        projectID,
		ProjectMountMode: projectMountMode,
		Project:          spec.Project,
		LLM:              spec.LLM,
		Startup:          spec.Startup,
		Tools:            spec.Tools,
		Environment:      spec.Environment,
		Credentials:      spec.Credentials,
		Networking:       spec.Networking,
		Directories:      spec.Directories,
		Resources:        spec.Resources,
		Labels:           labels,
	}
}

func localDockerAgentYAMLMetadataLabels(spec localDockerAgentYAMLSpec) map[string]string {
	labels := mergeStringMap(nil, spec.Labels)
	setDefaultLabel := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if labels == nil {
			labels = map[string]string{}
		}
		if strings.TrimSpace(labels[key]) == "" {
			labels[key] = value
		}
	}

	// WHY: ad-hoc YAML-created containers may later be registered manually from
	// Caesar. Mirror presentation metadata into labels so the register action uses
	// the same description/category/avatar as YAML-driven registration.
	setDefaultLabel("a2gent.agent_name", firstNonEmptyLocalAgentString(spec.Registry.AgentName, spec.Name))
	setDefaultLabel("a2gent.agent_emoji", spec.Emoji)
	setDefaultLabel("a2gent.agent_description", firstNonEmptyLocalAgentString(spec.Description, spec.Registry.Description))
	setDefaultLabel("a2gent.agent_category", firstNonEmptyLocalAgentString(spec.Category, spec.Registry.Category))
	setDefaultLabel("a2gent.agent_icon_url", spec.IconURL)
	setDefaultLabel("a2gent.agent_avatar_url", firstNonEmptyLocalAgentString(spec.AvatarURL, spec.Registry.AvatarURL, spec.Registry.AvatarPath, spec.IconURL))
	return labels
}

func localDockerAgentYAMLRegistryEnabled(registry localDockerAgentYAMLRegistry) bool {
	if registry.Enabled == nil {
		return false
	}
	return *registry.Enabled
}

func (spec localDockerAgentYAMLSpec) toRegisterRequest() registerLocalDockerAgentRequest {
	registry := spec.Registry
	// WHY: YAML-driven registration should be safe by default. A hidden registry
	// record enables private A2A/tunnel use; public discovery requires an explicit
	// `discoverable: true` plus Square owner approval outside this launcher.
	discoverable := false
	if registry.Discoverable != nil {
		discoverable = *registry.Discoverable
	}
	agentName := strings.TrimSpace(registry.AgentName)
	if agentName == "" {
		agentName = strings.TrimSpace(spec.Name)
	}
	description := firstNonEmptyLocalAgentString(registry.Description, spec.Description)
	category := firstNonEmptyLocalAgentString(registry.Category, spec.Category)
	avatarURL := firstNonEmptyLocalAgentString(registry.AvatarURL, spec.AvatarURL, registry.AvatarPath, spec.IconURL)
	agentHandle := firstNonEmptyLocalAgentString(registry.AgentHandle, registry.AgentID, registry.PublicID)
	if agentHandle == "" {
		agentHandle = strings.TrimSpace(spec.Name)
	}
	return registerLocalDockerAgentRequest{
		RegistryURL:        strings.TrimSpace(registry.RegistryURL),
		OwnerEmail:         strings.TrimSpace(registry.OwnerEmail),
		AgentName:          agentName,
		AgentHandle:        strings.TrimSpace(agentHandle),
		AgentID:            strings.TrimSpace(registry.AgentID),
		PublicID:           strings.TrimSpace(registry.PublicID),
		OrganizationHandle: strings.TrimSpace(registry.OrganizationHandle),
		Description:        description,
		NetworkAccess:      strings.TrimSpace(registry.NetworkAccess),
		EndpointURL:        strings.TrimSpace(registry.EndpointURL),
		AgentType:          strings.TrimSpace(registry.AgentType),
		Category:           category,
		Discoverable:       &discoverable,
		OfficialWebsite:    strings.TrimSpace(registry.OfficialWebsite),
		AvatarURL:          avatarURL,
		SupportsAudio:      registry.SupportsAudio,
		SupportsImages:     registry.SupportsImages,
		SupportsVideo:      registry.SupportsVideo,
		PricingModel:       strings.TrimSpace(registry.PricingModel),
		PricePerRequest:    registry.PricePerRequest,
		PricePerInputKB:    registry.PricePerInputKB,
		PricePerOutputKB:   registry.PricePerOutputKB,
		PricePerSession:    registry.PricePerSession,
		Currency:           strings.TrimSpace(registry.Currency),
		ConfigureContainer: registry.ConfigureContainer,
		Transport:          strings.TrimSpace(registry.Transport),
		SquareGRPCAddr:     strings.TrimSpace(registry.SquareGRPCAddr),
		SquareWSURL:        strings.TrimSpace(registry.SquareWSURL),
	}
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

func (s *Server) handleCreateLocalDockerAgentsFromYAML(w http.ResponseWriter, r *http.Request) {
	var req createLocalDockerAgentsFromYAMLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	raw := []byte(req.ConfigYAML)
	configPath := ""
	if strings.TrimSpace(req.ConfigPath) != "" {
		loaded, resolved, err := readLocalDockerAgentYAMLConfigFile(req.ConfigPath, "")
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		raw = loaded
		configPath = resolved
	}
	result, status, err := s.createLocalDockerAgentsFromYAML(r.Context(), raw, configPath)
	if err != nil {
		s.errorResponse(w, status, err.Error())
		return
	}
	s.jsonResponse(w, status, result)
}
