// Package agentdef defines the canonical, portable agent definition shared by
// local Docker agents and remote A2A agents. The model can be
// stored in the DB, exported to YAML, imported from YAML, and later published
// as a Square template (see docs/unified-agent-runtime-plan.md).
package agentdef

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Runtime types supported by the unified agent model.
const (
	RuntimeDocker = "docker"
	RuntimeHost   = "host" // legacy YAML import marker; local execution is Docker.
	RuntimeRemote = "remote"
)

// Workspace scopes control which project folders an agent can see.
const (
	WorkspaceScopeNone              = "none"
	WorkspaceScopeCurrentProject    = "current_project"
	WorkspaceScopeConfiguredProject = "configured_project"
	WorkspaceScopeSelectedProjects  = "selected_projects"
	WorkspaceScopeAllProjects       = "all_projects"
	WorkspaceScopeExplicitVolumes   = "explicit_volumes"
	WorkspaceScopeSnapshot          = "snapshot"
)

// Workspace mount modes.
const (
	WorkspaceMountRO = "ro"
	WorkspaceMountRW = "rw"
)

// Tool selection modes.
const (
	ToolsModeAll   = "all"
	ToolsModeAllow = "allow"
)

// CurrentVersion is the YAML schema version this package reads and writes.
const CurrentVersion = "1"

// Definition is the canonical unified agent definition.
type Definition struct {
	Version string    `yaml:"version" json:"version"`
	Agent   AgentMeta `yaml:"agent" json:"agent"`
	Runtime Runtime   `yaml:"runtime" json:"runtime"`
	LLM     LLM       `yaml:"llm,omitempty" json:"llm,omitempty"`
	// Metrics are compact orchestration hints for parent agents. Cost is a
	// relative expense score where lower is cheaper; speed and intelligence are
	// relative capability scores where higher is better.
	Metrics      AgentMetrics `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	Instructions Instructions `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	Workspace    Workspace    `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Tools        Tools        `yaml:"tools,omitempty" json:"tools,omitempty"`
	Skills       Skills       `yaml:"skills,omitempty" json:"skills,omitempty"`
	MCP          MCP          `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	Secrets      Secrets      `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Networking   Networking   `yaml:"networking,omitempty" json:"networking,omitempty"`
	Publish      Publish      `yaml:"publish,omitempty" json:"publish,omitempty"`
	// Local holds machine-specific bindings (project IDs, ports, credential
	// references). It must be stripped before publishing to Square.
	Local Local `yaml:"local,omitempty" json:"local,omitempty"`
}

// AgentMeta describes identity and presentation of the agent.
type AgentMeta struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Emoji       string `yaml:"emoji,omitempty" json:"emoji,omitempty"`
	IconURL     string `yaml:"icon_url,omitempty" json:"icon_url,omitempty"`
	AvatarURL   string `yaml:"avatar_url,omitempty" json:"avatar_url,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Kind        string `yaml:"kind,omitempty" json:"kind,omitempty"`
}

// Runtime selects and configures the execution backend.
type Runtime struct {
	Type      string    `yaml:"type" json:"type"`
	Image     string    `yaml:"image,omitempty" json:"image,omitempty"`
	Lifecycle string    `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	Resources Resources `yaml:"resources,omitempty" json:"resources,omitempty"`
}

// Resources limits a Docker runtime container.
type Resources struct {
	CPUs   string `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
	GPUs   string `yaml:"gpus,omitempty" json:"gpus,omitempty"`
}

// AgentMetrics exposes numeric routing hints for orchestration. Cost is an
// expense score where lower is cheaper; speed and intelligence are capability
// scores where higher is better. All values are expected on a compact 0-100
// scale so parent agents can compare agents without parsing prose.
type AgentMetrics struct {
	Cost         int    `yaml:"cost,omitempty" json:"cost,omitempty"`
	Speed        int    `yaml:"speed,omitempty" json:"speed,omitempty"`
	Intelligence int    `yaml:"intelligence,omitempty" json:"intelligence,omitempty"`
	Source       string `yaml:"source,omitempty" json:"source,omitempty"`
}

func (m AgentMetrics) IsZero() bool {
	return m.Cost == 0 && m.Speed == 0 && m.Intelligence == 0 && strings.TrimSpace(m.Source) == ""
}

func (m AgentMetrics) CompactString() string {
	parts := []string{}
	if m.Cost > 0 {
		parts = append(parts, fmt.Sprintf("cost=%d", m.Cost))
	}
	if m.Speed > 0 {
		parts = append(parts, fmt.Sprintf("speed=%d", m.Speed))
	}
	if m.Intelligence > 0 {
		parts = append(parts, fmt.Sprintf("intelligence=%d", m.Intelligence))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// LLM selects the provider/model the agent uses.

type LLM struct {
	Provider        string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model           string `yaml:"model,omitempty" json:"model,omitempty"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
}

// Instructions hold the system prompt and configurable instruction blocks.
type Instructions struct {
	System     string             `yaml:"system,omitempty" json:"system,omitempty"`
	SystemFile string             `yaml:"system_file,omitempty" json:"system_file,omitempty"`
	Blocks     []InstructionBlock `yaml:"blocks,omitempty" json:"blocks,omitempty"`
}

// InstructionBlock mirrors the sub-agent instruction block JSON shape.
type InstructionBlock struct {
	Type    string `yaml:"type" json:"type"`
	Value   string `yaml:"value,omitempty" json:"value,omitempty"`
	Enabled *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// Workspace declares the agent's file access policy.
type Workspace struct {
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`
	Mount string `yaml:"mount,omitempty" json:"mount,omitempty"`
}

// Tools declares the tool allow-list policy.
type Tools struct {
	Mode     string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	Enabled  []string `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Disabled []string `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// Skills lists required skills by name.
type Skills struct {
	ExternalMarkdown []string `yaml:"external_markdown,omitempty" json:"external_markdown,omitempty"`
	Integrations     []string `yaml:"integrations,omitempty" json:"integrations,omitempty"`
}

// MCP lists required MCP servers by name.
type MCP struct {
	Servers []string `yaml:"servers,omitempty" json:"servers,omitempty"`
}

// Secrets lists required secret names (never values).
type Secrets struct {
	Required []string `yaml:"required,omitempty" json:"required,omitempty"`
}

// Networking declares network policy for isolated runtimes.
type Networking struct {
	InternetAccess *bool `yaml:"internet_access,omitempty" json:"internet_access,omitempty"`
}

// Publish carries Square template publishing metadata.
type Publish struct {
	Square PublishSquare `yaml:"square,omitempty" json:"square,omitempty"`
}

// PublishSquare configures Square marketplace metadata.
type PublishSquare struct {
	Category     string `yaml:"category,omitempty" json:"category,omitempty"`
	IconURL      string `yaml:"icon_url,omitempty" json:"icon_url,omitempty"`
	AvatarURL    string `yaml:"avatar_url,omitempty" json:"avatar_url,omitempty"`
	Discoverable bool   `yaml:"discoverable,omitempty" json:"discoverable,omitempty"`
}

// Local holds machine-specific installation bindings that must never be
// published: resolved project IDs, host ports, and credential references.
type Local struct {
	HostPort        int                   `yaml:"host_port,omitempty" json:"host_port,omitempty"`
	ProjectBindings map[string]string     `yaml:"project_bindings,omitempty" json:"project_bindings,omitempty"`
	Credentials     map[string]Credential `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	// DefinitionDir is a local source folder for this agent definition. It is
	// mounted read-only into Docker agents at /soul/agents/<agent-id> so YAML,
	// skills, and related settings can be edited from Soul without entering the
	// container.
	DefinitionDir string `yaml:"definition_dir,omitempty" json:"definition_dir,omitempty"`
}

// Credential references a secret by environment variable or file, never value.
type Credential struct {
	Env  string `yaml:"env,omitempty" json:"env,omitempty"`
	File string `yaml:"file,omitempty" json:"file,omitempty"`
}

var validRuntimeTypes = map[string]struct{}{
	RuntimeDocker: {},
	RuntimeHost:   {},
	RuntimeRemote: {},
}

var validWorkspaceScopes = map[string]struct{}{
	WorkspaceScopeNone:              {},
	WorkspaceScopeCurrentProject:    {},
	WorkspaceScopeConfiguredProject: {},
	WorkspaceScopeSelectedProjects:  {},
	WorkspaceScopeAllProjects:       {},
	WorkspaceScopeExplicitVolumes:   {},
	WorkspaceScopeSnapshot:          {},
}

// Normalize trims fields and applies defaults so stored/parsed definitions are
// uniform: version 1, Docker runtime default, lowercase enum values.
func (d *Definition) Normalize() {
	if d == nil {
		return
	}
	d.Version = strings.TrimSpace(d.Version)
	if d.Version == "" {
		d.Version = CurrentVersion
	}
	d.Agent.ID = strings.TrimSpace(d.Agent.ID)
	d.Agent.Name = strings.TrimSpace(d.Agent.Name)
	d.Agent.Emoji = strings.TrimSpace(d.Agent.Emoji)
	d.Agent.IconURL = strings.TrimSpace(d.Agent.IconURL)
	d.Agent.AvatarURL = strings.TrimSpace(d.Agent.AvatarURL)
	d.Agent.Description = strings.TrimSpace(d.Agent.Description)
	d.Agent.Kind = strings.TrimSpace(d.Agent.Kind)
	d.Runtime.Type = strings.ToLower(strings.TrimSpace(d.Runtime.Type))
	if d.Runtime.Type == "" {
		d.Runtime.Type = RuntimeDocker
	}
	d.Workspace.Scope = strings.ToLower(strings.TrimSpace(d.Workspace.Scope))
	d.Workspace.Mount = strings.ToLower(strings.TrimSpace(d.Workspace.Mount))
	if d.Workspace.Scope != "" && d.Workspace.Mount == "" {
		d.Workspace.Mount = WorkspaceMountRO
	}
	d.Tools.Mode = strings.ToLower(strings.TrimSpace(d.Tools.Mode))
	if d.Tools.Mode == "" {
		if len(d.Tools.Enabled) > 0 {
			d.Tools.Mode = ToolsModeAllow
		} else {
			d.Tools.Mode = ToolsModeAll
		}
	}
	d.LLM.Provider = strings.TrimSpace(d.LLM.Provider)
	d.LLM.Model = strings.TrimSpace(d.LLM.Model)
	d.LLM.ReasoningEffort = strings.TrimSpace(d.LLM.ReasoningEffort)
	d.Instructions.SystemFile = strings.TrimSpace(d.Instructions.SystemFile)
	d.Metrics.Source = strings.TrimSpace(d.Metrics.Source)
	d.Local.DefinitionDir = strings.TrimSpace(d.Local.DefinitionDir)
	d.Publish.Square.Category = strings.ToLower(strings.TrimSpace(d.Publish.Square.Category))
	d.Publish.Square.IconURL = strings.TrimSpace(d.Publish.Square.IconURL)
	d.Publish.Square.AvatarURL = strings.TrimSpace(d.Publish.Square.AvatarURL)
}

// Validate checks enum fields and required identity after Normalize.
func (d *Definition) Validate() error {
	if d == nil {
		return fmt.Errorf("agent definition is empty")
	}
	if d.Version != CurrentVersion && d.Version != "1.0" {
		return fmt.Errorf("unsupported agent definition version %q", d.Version)
	}
	if d.Agent.Name == "" && d.Agent.ID == "" {
		return fmt.Errorf("agent.id or agent.name is required")
	}
	if _, ok := validRuntimeTypes[d.Runtime.Type]; !ok {
		return fmt.Errorf("runtime.type must be docker, remote, or legacy host (got %q)", d.Runtime.Type)
	}
	if d.Workspace.Scope != "" {
		if _, ok := validWorkspaceScopes[d.Workspace.Scope]; !ok {
			return fmt.Errorf("workspace.scope %q is not supported", d.Workspace.Scope)
		}
	}
	if d.Workspace.Mount != "" && d.Workspace.Mount != WorkspaceMountRO && d.Workspace.Mount != WorkspaceMountRW {
		return fmt.Errorf("workspace.mount must be ro or rw (got %q)", d.Workspace.Mount)
	}
	if d.Tools.Mode != "" && d.Tools.Mode != ToolsModeAll && d.Tools.Mode != ToolsModeAllow {
		return fmt.Errorf("tools.mode must be all or allow (got %q)", d.Tools.Mode)
	}
	if filepath.IsAbs(d.Instructions.SystemFile) || d.Instructions.SystemFile == "." || d.Instructions.SystemFile == ".." || strings.HasPrefix(d.Instructions.SystemFile, "../") {
		return fmt.Errorf("instructions.system_file must be a relative file path inside the agent definition folder")
	}
	if err := validateAgentMetrics(d.Metrics); err != nil {
		return err
	}
	return nil
}

func validateAgentMetrics(metrics AgentMetrics) error {
	values := []struct {
		name  string
		value int
	}{
		{name: "cost", value: metrics.Cost},
		{name: "speed", value: metrics.Speed},
		{name: "intelligence", value: metrics.Intelligence},
	}
	for _, item := range values {
		if item.value < 0 || item.value > 100 {
			return fmt.Errorf("metrics.%s must be between 0 and 100 (got %d)", item.name, item.value)
		}
	}
	return nil
}
