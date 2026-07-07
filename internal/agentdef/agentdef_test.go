package agentdef

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestParseYAMLPlanExample(t *testing.T) {
	raw := `
version: "1"

agent:
  id: code-reviewer
  name: Code Reviewer
  emoji: "🔎"
  description: Reviews code changes for correctness and regressions.
  kind: reviewer

runtime:
  type: docker
  image: a2gent-brute:latest
  lifecycle: warm
  resources:
    cpus: "2"
    memory: 2g

llm:
  provider: openai
  model: gpt-5.5

instructions:
  system: |
    You are a focused code reviewer.
  blocks:
    - type: builtin_tools
      enabled: true
    - type: text
      value: Prefer concise actionable findings.

workspace:
  scope: current_project
  mount: ro

tools:
  mode: allow
  enabled:
    - read
    - grep
  disabled:
    - delegate_to_agent

networking:
  internet_access: true

publish:
  square:
    category: engineering
    discoverable: false
`
	def, err := ParseYAML([]byte(raw))
	if err != nil {
		t.Fatalf("ParseYAML failed: %v", err)
	}
	if def.Agent.ID != "code-reviewer" || def.Runtime.Type != RuntimeDocker {
		t.Fatalf("unexpected definition: %+v", def)
	}
	if def.Runtime.Image != "a2gent-brute:latest" || def.Runtime.Resources.Memory != "2g" {
		t.Fatalf("runtime not parsed: %+v", def.Runtime)
	}
	if len(def.Instructions.Blocks) != 2 || def.Instructions.Blocks[1].Value != "Prefer concise actionable findings." {
		t.Fatalf("instruction blocks not parsed: %+v", def.Instructions.Blocks)
	}
	if def.Workspace.Scope != WorkspaceScopeCurrentProject || def.Workspace.Mount != WorkspaceMountRO {
		t.Fatalf("workspace not parsed: %+v", def.Workspace)
	}
	if def.Networking.InternetAccess == nil || !*def.Networking.InternetAccess {
		t.Fatalf("networking not parsed: %+v", def.Networking)
	}
	if def.Publish.Square.Category != "engineering" {
		t.Fatalf("publish not parsed: %+v", def.Publish)
	}
}

func TestParseYAMLAgentMetrics(t *testing.T) {
	raw := `
version: "1"
agent:
  id: router-helper
runtime:
  type: docker
metrics:
  cost: 25
  speed: 80
  intelligence: 70
  source: artificialanalysis.ai normalized model metrics
`
	def, err := ParseYAML([]byte(raw))
	if err != nil {
		t.Fatalf("ParseYAML failed: %v", err)
	}
	if def.Metrics.Cost != 25 || def.Metrics.Speed != 80 || def.Metrics.Intelligence != 70 {
		t.Fatalf("metrics not parsed: %+v", def.Metrics)
	}
	if got := def.Metrics.CompactString(); got != "cost=25, speed=80, intelligence=70" {
		t.Fatalf("unexpected compact metrics string %q", got)
	}
}

func TestParseYAMLRejectsOutOfRangeAgentMetrics(t *testing.T) {
	_, err := ParseYAML([]byte("agent:\n  id: x\nmetrics:\n  cost: 101\n"))
	if err == nil || !strings.Contains(err.Error(), "metrics.cost") {
		t.Fatalf("expected metrics.cost error, got: %v", err)
	}
}

func TestParseYAMLRejectsBadRuntime(t *testing.T) {
	_, err := ParseYAML([]byte("version: \"1\"\nagent:\n  id: x\nruntime:\n  type: vm\n"))
	if err == nil || !strings.Contains(err.Error(), "runtime.type") {
		t.Fatalf("expected runtime.type error, got: %v", err)
	}
}

func TestParseYAMLDefaultsToDockerRuntime(t *testing.T) {
	def, err := ParseYAML([]byte("agent:\n  id: x\n"))
	if err != nil {
		t.Fatalf("ParseYAML failed: %v", err)
	}
	if def.Runtime.Type != RuntimeDocker {
		t.Fatalf("expected default docker runtime, got %q", def.Runtime.Type)
	}
	if def.Version != CurrentVersion {
		t.Fatalf("expected default version, got %q", def.Version)
	}
}

func TestSubAgentRoundTrip(t *testing.T) {
	projectID := "bb113706-4903-40b7-8966-a23eb10ae220"
	sa := &storage.SubAgent{
		ID:                "sa-1",
		Name:              "Code Reviewer",
		ProjectID:         &projectID,
		Provider:          "openai",
		Model:             "gpt-5.5",
		EnabledTools:      []string{"read", "grep"},
		InstructionBlocks: `[{"type":"builtin_tools","enabled":true},{"type":"text","value":"Be concise."}]`,
	}

	def, err := FromSubAgent(sa)
	if err != nil {
		t.Fatalf("FromSubAgent failed: %v", err)
	}
	if def.Runtime.Type != RuntimeDocker {
		t.Fatalf("expected docker runtime for migrated sub-agent, got %q", def.Runtime.Type)
	}
	if def.Workspace.Scope != WorkspaceScopeConfiguredProject {
		t.Fatalf("expected configured_project scope, got %q", def.Workspace.Scope)
	}
	if def.Local.ProjectBindings[WorkspaceScopeConfiguredProject] != projectID {
		t.Fatalf("project binding missing: %+v", def.Local)
	}

	raw, err := ToYAML(def)
	if err != nil {
		t.Fatalf("ToYAML failed: %v", err)
	}
	parsed, err := ParseYAML(raw)
	if err != nil {
		t.Fatalf("ParseYAML of exported YAML failed: %v\nyaml:\n%s", err, raw)
	}

	back, err := ToSubAgentConfig(parsed)
	if err != nil {
		t.Fatalf("ToSubAgentConfig failed: %v", err)
	}
	if back.ID != sa.ID || back.Name != sa.Name || back.Provider != sa.Provider || back.Model != sa.Model {
		t.Fatalf("round trip lost identity: %+v", back)
	}
	if len(back.EnabledTools) != 2 || back.EnabledTools[0] != "read" {
		t.Fatalf("round trip lost tools: %+v", back.EnabledTools)
	}
	if back.ProjectID == nil || *back.ProjectID != projectID {
		t.Fatalf("round trip lost project binding: %+v", back.ProjectID)
	}
	if !strings.Contains(back.InstructionBlocks, "builtin_tools") || !strings.Contains(back.InstructionBlocks, "Be concise.") {
		t.Fatalf("round trip lost instruction blocks: %s", back.InstructionBlocks)
	}
}

func TestToSubAgentConfigWorksForAnyRuntime(t *testing.T) {
	def := &Definition{
		Agent:   AgentMeta{ID: "x", Name: "X"},
		Runtime: Runtime{Type: RuntimeDocker},
		LLM:     LLM{Provider: "openai", Model: "gpt-5.5"},
	}
	cfg, err := ToSubAgentConfig(def)
	if err != nil {
		t.Fatalf("ToSubAgentConfig failed for docker runtime: %v", err)
	}
	if cfg.Provider != "openai" || cfg.Model != "gpt-5.5" {
		t.Fatalf("config lost LLM fields: %+v", cfg)
	}
}

func TestSubAgentAllToolsRoundTrip(t *testing.T) {
	sa := &storage.SubAgent{ID: "sa-2", Name: "Helper", InstructionBlocks: "[]"}
	def, err := FromSubAgent(sa)
	if err != nil {
		t.Fatalf("FromSubAgent failed: %v", err)
	}
	if def.Tools.Mode != ToolsModeAll {
		t.Fatalf("expected all tools mode, got %q", def.Tools.Mode)
	}
	back, err := ToSubAgentConfig(def)
	if err != nil {
		t.Fatalf("ToSubAgentConfig failed: %v", err)
	}
	if len(back.EnabledTools) != 0 {
		t.Fatalf("expected empty enabled tools (= all), got %+v", back.EnabledTools)
	}
}

func TestStripLocal(t *testing.T) {
	def := &Definition{
		Agent:   AgentMeta{ID: "x", Name: "X"},
		Runtime: Runtime{Type: RuntimeHost},
		Local: Local{
			HostPort:        18080,
			ProjectBindings: map[string]string{WorkspaceScopeConfiguredProject: "abc"},
		},
	}
	stripped := StripLocal(def)
	if stripped.Local.HostPort != 0 || stripped.Local.ProjectBindings != nil {
		t.Fatalf("local section not stripped: %+v", stripped.Local)
	}
	if def.Local.HostPort != 18080 {
		t.Fatal("original definition mutated")
	}
}
