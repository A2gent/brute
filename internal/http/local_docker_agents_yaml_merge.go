package http

import "strings"

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
