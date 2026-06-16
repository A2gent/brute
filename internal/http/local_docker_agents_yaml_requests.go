package http

import "strings"

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
