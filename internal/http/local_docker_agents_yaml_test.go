package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func resetLocalDockerPortReservationsForTest() {
	localDockerPortReservations.Lock()
	localDockerPortReservations.ports = map[int]struct{}{}
	localDockerPortReservations.Unlock()
}

func TestParseLocalDockerAgentYAMLConfigSingleAgent(t *testing.T) {
	raw := `version: 1
agent:
  name: review-bot
  description: Reviews code with a strict checklist.
  emoji: "🔍"
  icon_url: https://example.com/icon.png
  avatar_url: https://example.com/avatar.png
  category: engineering
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
	req := agent.toCreateRequest()
	if req.Labels["a2gent.agent_description"] != "Reviews code with a strict checklist." || req.Labels["a2gent.agent_category"] != "engineering" {
		t.Fatalf("presentation metadata was not mirrored to labels: %#v", req.Labels)
	}
	if req.Labels["a2gent.agent_avatar_url"] != "https://example.com/avatar.png" || req.Labels["a2gent.agent_icon_url"] != "https://example.com/icon.png" {
		t.Fatalf("icon metadata was not mirrored to labels: %#v", req.Labels)
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
func TestReadLocalDockerAgentYAMLConfigFileAcceptsDefinitionFolder(t *testing.T) {
	dir := t.TempDir()
	definitionDir := filepath.Join(dir, "reviewer")
	if err := os.MkdirAll(definitionDir, 0o755); err != nil {
		t.Fatalf("failed to create definition dir: %v", err)
	}
	configPath := filepath.Join(definitionDir, "agent.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nagent:\n  name: folder-reviewer\n"), 0o644); err != nil {
		t.Fatalf("failed to write agent.yaml: %v", err)
	}

	raw, resolved, err := readLocalDockerAgentYAMLConfigFile(definitionDir, "")
	if err != nil {
		t.Fatalf("readLocalDockerAgentYAMLConfigFile returned error: %v", err)
	}
	if resolved != configPath {
		t.Fatalf("expected folder config %s, got %s", configPath, resolved)
	}
	if !strings.Contains(string(raw), "folder-reviewer") {
		t.Fatalf("unexpected config content: %s", raw)
	}
}

func TestAppendLocalDockerAgentDefinitionDirArgsMountsSoulDefinitionAndSkills(t *testing.T) {
	definitionDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(definitionDir, "skills"), 0o755); err != nil {
		t.Fatalf("failed to create skills dir: %v", err)
	}

	args, err := appendLocalDockerAgentDefinitionDirArgs([]string{"run"}, definitionDir, "Dev Reviewer")
	if err != nil {
		t.Fatalf("appendLocalDockerAgentDefinitionDirArgs returned error: %v", err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		definitionDir + ":/soul/agents/dev-reviewer:ro",
		"A2GENT_AGENT_DEFINITION_DIR=/soul/agents/dev-reviewer",
		"AAGENT_AGENT_DEFINITION_DIR=/soul/agents/dev-reviewer",
		"AAGENT_SKILLS_FOLDER=/soul/agents/dev-reviewer/skills",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected docker args to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestLocalDockerAgentYAMLBuildsDockerArgsForToolsAndRuntimeOptions(t *testing.T) {
	server := &Server{toolManager: nil}
	req := createLocalDockerAgentRequest{
		LLM: localDockerAgentYAMLLLM{
			Provider:        "openai",
			Model:           "gpt-5.5",
			ReasoningEffort: "high",
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
		"A2GENT_SYNC_DISABLED_TOOLS_FROM_ENV=true",
		"A2GENT_DISABLE_TOOLS_BY_DEFAULT=false",
		"FOO=bar",
		"TOKEN=secret",
		"AAGENT_PROVIDER=openai",
		"AAGENT_MODEL=gpt-5.5",
		"AAGENT_OPENAI_CODEX_REASONING_EFFORT=high",
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

func TestResolveLocalDockerAgentProxyLLMInheritsActiveProviderAndModel(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	server.config.ActiveProvider = string(config.ProviderCursor)
	server.config.Providers[string(config.ProviderCursor)] = config.Provider{
		Name:  string(config.ProviderCursor),
		Model: "composer-2.5",
	}

	resolved := server.resolveLocalDockerAgentProxyLLM(createLocalDockerAgentRequest{})
	if resolved.Provider != string(config.ProviderCursor) {
		t.Fatalf("provider = %q, want %q", resolved.Provider, config.ProviderCursor)
	}
	if resolved.Model != "composer-2.5" {
		t.Fatalf("model = %q, want composer-2.5", resolved.Model)
	}
}

func TestResolveLocalDockerAgentProxyLLMPreservesExplicitTarget(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	server.config.ActiveProvider = string(config.ProviderCursor)

	resolved := server.resolveLocalDockerAgentProxyLLM(createLocalDockerAgentRequest{
		LLM: localDockerAgentYAMLLLM{
			Provider: string(config.ProviderOpenRouter),
			Model:    "anthropic/claude-sonnet-4",
		},
	})
	if resolved.Provider != string(config.ProviderOpenRouter) {
		t.Fatalf("provider = %q, want %q", resolved.Provider, config.ProviderOpenRouter)
	}
	if resolved.Model != "anthropic/claude-sonnet-4" {
		t.Fatalf("model = %q, want anthropic/claude-sonnet-4", resolved.Model)
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
    avatar_url: https://example.com/avatar.png
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
	if req.AvatarURL != "https://example.com/avatar.png" {
		t.Fatalf("registry request avatar_url not parsed: %#v", req)
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

func TestReserveAvailableLocalDockerPortSkipsInProcessReservations(t *testing.T) {
	resetLocalDockerPortReservationsForTest()
	t.Cleanup(resetLocalDockerPortReservationsForTest)

	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command == "docker" && len(args) > 0 && args[0] == "ps" {
			return "", nil
		}
		return "", nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })
	oldHostPortListenable := hostPortListenable
	hostPortListenable = func(port int) bool { return true }
	t.Cleanup(func() { hostPortListenable = oldHostPortListenable })

	first, releaseFirst, err := reserveAvailableLocalDockerPort(context.Background(), 18080, 18082)
	if err != nil {
		t.Fatalf("first reservation failed: %v", err)
	}
	defer releaseFirst()
	second, releaseSecond, err := reserveAvailableLocalDockerPort(context.Background(), 18080, 18082)
	if err != nil {
		t.Fatalf("second reservation failed: %v", err)
	}
	defer releaseSecond()
	if first == second {
		t.Fatalf("parallel reservations must not receive the same port: %d", first)
	}
}

func TestCreateLocalDockerAgentRetriesAutoPortCollision(t *testing.T) {
	resetLocalDockerPortReservationsForTest()
	t.Cleanup(resetLocalDockerPortReservationsForTest)

	server, _ := newUnifiedAgentsTestServer(t)
	oldRunCommand := runCommand
	oldHostPortListenable := hostPortListenable
	hostPortListenable = func(port int) bool { return true }
	t.Cleanup(func() { hostPortListenable = oldHostPortListenable })
	var runPorts []string
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command != "docker" {
			return "", nil
		}
		if len(args) > 0 && args[0] == "ps" {
			if len(runPorts) == 0 {
				return "", nil
			}
			row := dockerPSRow{
				ID:     "container-id",
				Image:  "a2gent-brute:latest",
				State:  "running",
				Status: "Up",
				Names:  "retry-agent",
				Ports:  "0.0.0.0:" + runPorts[len(runPorts)-1] + "->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue,
			}
			encoded, _ := json.Marshal(row)
			return string(encoded), nil
		}
		if len(args) > 0 && args[0] == "run" {
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "--publish" {
					runPorts = append(runPorts, strings.TrimSuffix(args[i+1], ":8080"))
					break
				}
			}
			if len(runPorts) == 1 {
				return "", fmt.Errorf("docker: Error response from daemon: Bind for 0.0.0.0:%s failed: port is already allocated", runPorts[0])
			}
			return "container-id", nil
		}
		if len(args) > 0 && args[0] == "rm" {
			return "", nil
		}
		return "", nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })

	result, status, err := server.createLocalDockerAgent(context.Background(), createLocalDockerAgentRequest{
		Name:        "retry-agent",
		Image:       "a2gent-brute:latest",
		Directories: localDockerAgentYAMLDirectories{Data: t.TempDir()},
	})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("createLocalDockerAgent failed: status=%d err=%v", status, err)
	}
	if result == nil || result.Agent == nil || result.Agent.HostPort == 0 {
		t.Fatalf("expected created agent details after retry, got %#v", result)
	}
	if len(runPorts) != 2 {
		t.Fatalf("expected docker run to be retried once, got ports %#v", runPorts)
	}
	if runPorts[0] == runPorts[1] {
		t.Fatalf("retry should use a different host port, got %#v", runPorts)
	}
}

func TestRegisterLocalDockerAgentDefaultsMetadataFromContainerLabels(t *testing.T) {
	var got squareRegisterAgentRequest
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agents/register" {
			t.Fatalf("unexpected registry request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode registry request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"agent":{"id":"agent-1","name":%q,"agent_handle":"code-reviewer","public_id":"code-reviewer"},"api_key":"sq_key"}`, got.Name)
	}))
	defer registry.Close()

	server := &Server{}
	_, status, err := server.registerLocalDockerAgent(context.Background(), &LocalDockerAgent{
		ID:       "container-1",
		Name:     "agent-dev-code-reviewer",
		HostPort: 18080,
		APIURL:   "http://127.0.0.1:18080",
		Labels: map[string]string{
			"a2gent.agent_name":        "Code Reviewer",
			"a2gent.agent_description": "Reviews code changes for correctness and regressions.",
			"a2gent.agent_category":    "engineering",
			"a2gent.agent_avatar_url":  "https://example.com/avatar.png",
		},
	}, registerLocalDockerAgentRequest{
		RegistryURL:        registry.URL,
		OwnerEmail:         "owner@example.com",
		AgentHandle:        "code-reviewer",
		ConfigureContainer: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("registerLocalDockerAgent returned error: status=%d err=%v", status, err)
	}
	if got.Name != "Code Reviewer" || got.Description != "Reviews code changes for correctness and regressions." {
		t.Fatalf("container label identity metadata not used: %#v", got)
	}
	if got.Category != "engineering" || got.AvatarURL != "https://example.com/avatar.png" {
		t.Fatalf("container label publish metadata not used: %#v", got)
	}
}

func TestRegisterLocalDockerAgentDerivesValidHandleFromAgentNameLabel(t *testing.T) {
	var got squareRegisterAgentRequest
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agents/register" {
			t.Fatalf("unexpected registry request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode registry request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"agent":{"id":"agent-1","name":%q,"agent_handle":%q,"public_id":%q},"api_key":"sq_key"}`, got.Name, got.AgentHandle, got.AgentHandle)
	}))
	defer registry.Close()

	server := &Server{}
	_, status, err := server.registerLocalDockerAgent(context.Background(), &LocalDockerAgent{
		ID:       "container-1",
		Name:     "agent-dev-code-reviewer__project-bb113706-4903-40b7-8966-a23eb10ae220",
		HostPort: 18080,
		APIURL:   "http://127.0.0.1:18080",
		Labels: map[string]string{
			"a2gent.agent_name": "Code Reviewer",
		},
	}, registerLocalDockerAgentRequest{
		RegistryURL:        registry.URL,
		OwnerEmail:         "owner@example.com",
		ConfigureContainer: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("registerLocalDockerAgent returned error: status=%d err=%v", status, err)
	}
	if got.AgentHandle != "code-reviewer" {
		t.Fatalf("expected handle from agent name label, got %q", got.AgentHandle)
	}
}

func TestSlugifyForA2AgentHandleMatchesSquareHandleRules(t *testing.T) {
	cases := map[string]string{
		"YouTube Transcriber (Gemini)": "youtube-transcriber-gemini",
		"__Already_OK__":               "already_ok",
		"AI":                           "ai0",
		"!!!":                          "",
	}
	for input, want := range cases {
		if got := slugifyForA2AgentHandle(input); got != want {
			t.Fatalf("slugifyForA2AgentHandle(%q) = %q, want %q", input, got, want)
		}
	}

	long := slugifyForA2AgentHandle("Agent " + strings.Repeat("x", 80) + "_")
	if len(long) > 64 {
		t.Fatalf("handle length = %d, want <= 64", len(long))
	}
	if strings.HasSuffix(long, "-") || strings.HasSuffix(long, "_") {
		t.Fatalf("handle must not end with a separator: %q", long)
	}
}

func TestRegisterLocalDockerAgentUploadsLocalAvatarAsset(t *testing.T) {
	avatarPath := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(avatarPath, []byte("fake-png"), 0o644); err != nil {
		t.Fatalf("write avatar: %v", err)
	}

	var got squareRegisterAgentRequest
	uploaded := false
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/agents/register":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode registry request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"agent":{"id":"agent-1","name":"Avatar Bot","agent_handle":"avatar-bot","public_id":"avatar-bot"},"api_key":"sq_key"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/owner/agents/agent-1/avatar":
			if r.Header.Get("Authorization") != "Bearer sq_key" {
				t.Fatalf("avatar upload did not use registered agent API key: %q", r.Header.Get("Authorization"))
			}
			if err := r.ParseMultipartForm(9 << 20); err != nil {
				t.Fatalf("parse avatar multipart: %v", err)
			}
			file, _, err := r.FormFile("avatar")
			if err != nil {
				t.Fatalf("missing avatar form file: %v", err)
			}
			defer file.Close()
			body, _ := io.ReadAll(file)
			if string(body) != "fake-png" {
				t.Fatalf("unexpected uploaded avatar body: %q", string(body))
			}
			uploaded = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"message":"avatar uploaded"}`)
		default:
			t.Fatalf("unexpected registry request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer registry.Close()

	server := &Server{}
	_, status, err := server.registerLocalDockerAgent(context.Background(), &LocalDockerAgent{
		ID:       "container-1",
		Name:     "avatar-bot",
		HostPort: 18080,
		APIURL:   "http://127.0.0.1:18080",
		Labels: map[string]string{
			"a2gent.agent_name":       "Avatar Bot",
			"a2gent.agent_avatar_url": "http://localhost:5445/assets/images?path=" + avatarPath,
		},
	}, registerLocalDockerAgentRequest{
		RegistryURL:        registry.URL,
		OwnerEmail:         "owner@example.com",
		AgentHandle:        "avatar-bot",
		ConfigureContainer: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("registerLocalDockerAgent returned error: status=%d err=%v", status, err)
	}
	if got.AvatarURL != "" {
		t.Fatalf("local avatar asset URL must not be sent as public avatar_url: %#v", got)
	}
	if !uploaded {
		t.Fatalf("expected local avatar asset to be uploaded after registration")
	}
}
