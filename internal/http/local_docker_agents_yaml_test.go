package http

import (
	"strings"
	"testing"
)

func TestParseLocalDockerAgentYAMLConfigSingleAgent(t *testing.T) {
	raw := `version: 1
agent:
  name: review-bot
  image: a2gent-brute:dev
  host_port: 18111
  agent_kind: reviewer
  system_prompt: |
    You review code for regressions.
  session_id: sess-123
  project:
    id: project-app
    mount: rw
  llm:
    provider: openai
    model: gpt-5.5
    lm_studio_base_url: http://host.docker.internal:1234/v1
  tools:
    enabled:
      - read
      - grep
    disabled:
      - bash
  environment:
    FOO: bar
  credentials:
    OPENAI_API_KEY:
      env: OPENAI_API_KEY
    CUSTOM_TOKEN:
      value: literal-token
  networking:
    network: a2gent-dev
    aliases:
      - review-bot
    extra_hosts:
      - host.docker.internal:host-gateway
    publish:
      - host_port: 19090
        container_port: 9090
        protocol: tcp
  directories:
    data: /tmp/a2gent-review-data
    volumes:
      - host_path: /tmp/cache
        container_path: /cache
        mode: rw
  resources:
    cpus: "1.5"
    memory: 2g
    gpus: all
  labels:
    purpose: review
`

	cfg, err := parseLocalDockerAgentYAMLConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parseLocalDockerAgentYAMLConfig returned error: %v", err)
	}
	agents, err := cfg.expandAgents()
	if err != nil {
		t.Fatalf("expandAgents returned error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one agent, got %d", len(agents))
	}
	agent := agents[0]
	if agent.Name != "review-bot" || agent.Image != "a2gent-brute:dev" || agent.HostPort != 18111 {
		t.Fatalf("unexpected identity: %#v", agent)
	}
	if agent.AgentKind != "reviewer" || !strings.Contains(agent.SystemPrompt, "regressions") {
		t.Fatalf("agent role/prompt not parsed: %#v", agent)
	}
	if agent.Project.ID != "project-app" || agent.Project.Mount != "rw" {
		t.Fatalf("project not parsed: %#v", agent.Project)
	}
	if agent.LLM.Provider != "openai" || agent.LLM.Model != "gpt-5.5" {
		t.Fatalf("llm not parsed: %#v", agent.LLM)
	}
	if len(agent.Tools.Enabled) != 2 || agent.Tools.Enabled[0] != "read" || agent.Tools.Disabled[0] != "bash" {
		t.Fatalf("tools not parsed: %#v", agent.Tools)
	}
	if agent.Environment["FOO"] != "bar" || agent.Credentials["CUSTOM_TOKEN"].Value != "literal-token" {
		t.Fatalf("env/credentials not parsed: env=%#v credentials=%#v", agent.Environment, agent.Credentials)
	}
	if agent.Networking.Network != "a2gent-dev" || len(agent.Networking.Publish) != 1 || agent.Networking.Publish[0].ContainerPort != 9090 {
		t.Fatalf("networking not parsed: %#v", agent.Networking)
	}
	if agent.Directories.Data != "/tmp/a2gent-review-data" || len(agent.Directories.Volumes) != 1 {
		t.Fatalf("directories not parsed: %#v", agent.Directories)
	}
	if agent.Resources.CPUs != "1.5" || agent.Resources.Memory != "2g" || agent.Resources.GPUs != "all" {
		t.Fatalf("resources not parsed: %#v", agent.Resources)
	}
	if agent.Labels["purpose"] != "review" {
		t.Fatalf("labels not parsed: %#v", agent.Labels)
	}
}

func TestParseLocalDockerAgentYAMLConfigExpandsBatchDefaults(t *testing.T) {
	raw := `version: 1
defaults:
  image: a2gent-brute:latest
  name_prefix: team
  start_port: 18200
  session_id: sess-parent
  project:
    id: project-app
    mount: ro
  tools:
    enabled: [read]
agents:
  - agent_kind: researcher
    system_prompt: Research only.
  - name: explicit-planner
    agent_kind: planner
    host_port: 18333
`

	cfg, err := parseLocalDockerAgentYAMLConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parseLocalDockerAgentYAMLConfig returned error: %v", err)
	}
	agents, err := cfg.expandAgents()
	if err != nil {
		t.Fatalf("expandAgents returned error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected two agents, got %d", len(agents))
	}
	if agents[0].Image != "a2gent-brute:latest" || agents[0].HostPort != 18200 {
		t.Fatalf("first agent defaults not applied: %#v", agents[0])
	}
	if !strings.HasPrefix(agents[0].Name, "team-") || !strings.Contains(agents[0].Name, "researcher") {
		t.Fatalf("expected generated batch name with kind, got %q", agents[0].Name)
	}
	if agents[0].SessionID != "sess-parent" || agents[0].Project.ID != "project-app" || agents[0].Tools.Enabled[0] != "read" {
		t.Fatalf("shared defaults not applied: %#v", agents[0])
	}
	if agents[1].Name != "explicit-planner" || agents[1].HostPort != 18333 {
		t.Fatalf("explicit values should win: %#v", agents[1])
	}
}

func TestLocalDockerAgentSpecToCreateRequestPreservesLegacyFields(t *testing.T) {
	spec := localDockerAgentYAMLSpec{
		Name:             "dev-bot",
		Image:            "a2gent-brute:dev",
		HostPort:         18123,
		LMStudioBaseURL:  "http://lmstudio/v1",
		AgentKind:        "developer",
		SystemPrompt:     "Implement minimal changes.",
		SessionID:        "sess-1",
		ProjectID:        "project-1",
		ProjectMountMode: "rw",
		Tools: localDockerAgentYAMLTools{
			Enabled: []string{"read", "bash"},
		},
	}

	req := spec.toCreateRequest()
	if req.Name != spec.Name || req.Image != spec.Image || req.HostPort != spec.HostPort {
		t.Fatalf("identity not preserved: %#v", req)
	}
	if req.LMStudioBaseURL != spec.LMStudioBaseURL || req.AgentKind != spec.AgentKind || req.SystemPrompt != spec.SystemPrompt {
		t.Fatalf("agent fields not preserved: %#v", req)
	}
	if req.SessionID != spec.SessionID || req.ProjectID != spec.ProjectID || req.ProjectMountMode != spec.ProjectMountMode {
		t.Fatalf("session/project fields not preserved: %#v", req)
	}
	if len(req.Tools.Enabled) != 2 || req.Tools.Enabled[1] != "bash" {
		t.Fatalf("tools not preserved: %#v", req.Tools)
	}
}

func TestLocalDockerAgentYAMLBuildsDockerArgsForToolsAndRuntimeOptions(t *testing.T) {
	server := &Server{toolManager: nil}
	req := createLocalDockerAgentRequest{
		LLM: localDockerAgentYAMLLLM{
			Provider: "openai",
			Model:    "gpt-5.5",
		},
		Tools: localDockerAgentYAMLTools{Mode: "all"},
		Environment: map[string]string{
			"FOO": "bar",
		},
		Credentials: map[string]localDockerAgentCredential{
			"TOKEN": {Value: "secret"},
		},
		Networking: localDockerAgentYAMLNetworking{
			Network:    "a2gent-dev",
			ExtraHosts: []string{"host.docker.internal:host-gateway"},
			Publish: []localDockerAgentYAMLPortPublish{
				{HostPort: 19090, ContainerPort: 9090, Protocol: "tcp"},
			},
		},
		Directories: localDockerAgentYAMLDirectories{
			Volumes: []localDockerAgentYAMLVolumeMount{
				{HostPath: "cache", ContainerPath: "/cache", Mode: "rw"},
			},
		},
		Resources: localDockerAgentYAMLResources{CPUs: "1", Memory: "512m"},
	}

	args := []string{"run"}
	args, err := server.applyLocalDockerAgentToolsArgs(args, req.Tools)
	if err != nil {
		t.Fatalf("applyLocalDockerAgentToolsArgs returned error: %v", err)
	}
	args, err = appendLocalDockerAgentExtraArgs(args, req, "/tmp/agents")
	if err != nil {
		t.Fatalf("appendLocalDockerAgentExtraArgs returned error: %v", err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"A2GENT_DISABLE_TOOLS_BY_DEFAULT=false",
		"FOO=bar",
		"TOKEN=secret",
		"AAGENT_PROVIDER=openai",
		"AAGENT_MODEL=gpt-5.5",
		"a2gent-dev",
		"host.docker.internal:host-gateway",
		"19090:9090/tcp",
		"/tmp/agents/cache:/cache",
		"--cpus\n1",
		"--memory\n512m",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected docker args to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestLocalDockerAgentYAMLBuildsDockerModelRunnerArgs(t *testing.T) {
	req := createLocalDockerAgentRequest{
		LLM: localDockerAgentYAMLLLM{
			Provider: "dmr",
			Model:    "ai/qwen3",
		},
	}

	args, err := appendLocalDockerAgentExtraArgs([]string{"run"}, req, "")
	if err != nil {
		t.Fatalf("appendLocalDockerAgentExtraArgs returned error: %v", err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"AAGENT_PROVIDER=openai",
		"AAGENT_MODEL=ai/qwen3",
		"OPENAI_BASE_URL=" + dockerModelRunnerOpenAIBaseURL,
		"OPENAI_API_KEY=docker-model-runner",
		"model-runner.docker.internal:host-gateway",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected docker args to contain %q, got:\n%s", want, joined)
		}
	}
	if !localDockerAgentBypassesParentLLMProxy(req) {
		t.Fatalf("Docker Model Runner provider should bypass parent LLM proxy")
	}
}

func TestParseLocalDockerAgentYAMLConfigParsesStartupPrompt(t *testing.T) {
	raw := `version: 1
agent:
  name: prompt-bot
  startup:
    prompt: |
      Inspect the mounted workspace and summarize the first risks.
    auto_run: true
`

	cfg, err := parseLocalDockerAgentYAMLConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parseLocalDockerAgentYAMLConfig returned error: %v", err)
	}
	agents, err := cfg.expandAgents()
	if err != nil {
		t.Fatalf("expandAgents returned error: %v", err)
	}
	req := agents[0].toCreateRequest()
	if !strings.Contains(localDockerAgentStartupPrompt(req), "first risks") {
		t.Fatalf("startup prompt not parsed: %#v", req.Startup)
	}
	if !localDockerAgentStartupAutoRun(req) {
		t.Fatalf("startup auto_run not parsed: %#v", req.Startup)
	}
}

func TestParseLocalDockerAgentYAMLConfigRejectsProjectAndWorkspaceMount(t *testing.T) {
	raw := `version: 1
agent:
  name: invalid-bot
  project:
    id: project-app
  directories:
    workspace:
      host_path: .
      container_path: /workspace
`

	cfg, err := parseLocalDockerAgentYAMLConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parseLocalDockerAgentYAMLConfig returned error: %v", err)
	}
	_, err = cfg.expandAgents()
	if err == nil || !strings.Contains(err.Error(), "use either project/project_id or directories.workspace") {
		t.Fatalf("expected workspace/project validation error, got %v", err)
	}
}

func TestParseLocalDockerAgentYAMLConfigParsesRegistryMetadata(t *testing.T) {
	raw := `version: 1
agent:
  name: seo-bot
  registry:
    enabled: true
    registry_url: https://a2gent.net
    owner_email: owner@example.com
    agent_name: SEO Auditor
    agent_handle: seo-auditor
    description: Audits websites for SEO and accessibility.
    network_access: behind_nat
    agent_type: personal
    category: marketing
    discoverable: false
    official_website: https://example.com
    supports_audio: false
    supports_images: true
    supports_video: false
    price_per_session: 0.02
    currency: USD
`

	cfg, err := parseLocalDockerAgentYAMLConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parseLocalDockerAgentYAMLConfig returned error: %v", err)
	}
	agents, err := cfg.expandAgents()
	if err != nil {
		t.Fatalf("expandAgents returned error: %v", err)
	}
	if len(agents) != 1 || !localDockerAgentYAMLRegistryEnabled(agents[0].Registry) {
		t.Fatalf("registry config not parsed: %#v", agents)
	}
	req := agents[0].toRegisterRequest()
	if req.RegistryURL != "https://a2gent.net" || req.OwnerEmail != "owner@example.com" || req.AgentHandle != "seo-auditor" {
		t.Fatalf("registry request identity not parsed: %#v", req)
	}
	if req.Category != "marketing" || req.AgentType != "personal" || req.PricePerSession != 0.02 {
		t.Fatalf("registry request metadata not parsed: %#v", req)
	}
	if req.Discoverable == nil || *req.Discoverable {
		t.Fatalf("expected explicit hidden listing, got %#v", req.Discoverable)
	}
}

func TestLocalDockerAgentYAMLRegistryDefaultsToHiddenDiscoverability(t *testing.T) {
	raw := `version: 1
agent:
  name: private-seo-bot
  registry:
    enabled: true
`

	cfg, err := parseLocalDockerAgentYAMLConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parseLocalDockerAgentYAMLConfig returned error: %v", err)
	}
	agents, err := cfg.expandAgents()
	if err != nil {
		t.Fatalf("expandAgents returned error: %v", err)
	}
	req := agents[0].toRegisterRequest()
	if req.AgentName != "private-seo-bot" || req.AgentHandle != "private-seo-bot" {
		t.Fatalf("expected registry request to default identity from container name: %#v", req)
	}
	if req.Discoverable == nil || *req.Discoverable {
		t.Fatalf("YAML registration should default to hidden/non-discoverable, got %#v", req.Discoverable)
	}
}

func TestParsePublishedHostPorts(t *testing.T) {
	ports := "0.0.0.0:18080->8080/tcp, [::]:18080->8080/tcp, 9300/tcp, 127.0.0.1:19090-19091->9090-9091/tcp"
	got := map[int]bool{}
	for _, port := range parsePublishedHostPorts(ports) {
		got[port] = true
	}
	for _, port := range []int{18080, 19090, 19091} {
		if !got[port] {
			t.Fatalf("expected parsed host port %d from %q; got %#v", port, ports, got)
		}
	}
	if got[9300] {
		t.Fatalf("container-only ports should not be treated as published host ports: %#v", got)
	}
}
