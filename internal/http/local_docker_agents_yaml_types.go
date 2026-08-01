package http

const (
	localDockerAgentYAMLMaxAgents         = 100
	localDockerAgentYAMLDefaultNamePrefix = "a2gent-local"
	dockerModelRunnerOpenAIBaseURL        = "http://model-runner.docker.internal/engines/v1"
)

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
	SystemPromptFile string                                `yaml:"system_prompt_file" json:"system_prompt_file,omitempty"`
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
	ReasoningEffort string `yaml:"reasoning_effort" json:"reasoning_effort,omitempty"`
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
