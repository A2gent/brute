# Local Docker Agent YAML

Reusable local Brute agents can be described in YAML and started through either Caesar's **Create Local Agent** view or the `create_local_docker_agents_from_yaml` tool. Store reusable configs in Soul under `agents/*.yaml`; with the default Brute data path that is `~/.local/share/aagent/agents/*.yaml`.

The launcher is intentionally a Brute container launcher, not a replacement for Docker Agent. It maps the useful Docker agentic concepts to Brute runtime knobs:

- Docker Agent-style model selection maps to `llm.provider`, `llm.model`, and `llm.base_url`.
- Docker Model Runner is supported with `llm.provider: dmr`; the container uses Brute's OpenAI-compatible provider and `http://model-runner.docker.internal/engines/v1`.
- Docker/MCP-style tool boundaries map to Brute's `tools.enabled`, `tools.disabled`, and `tools.mode`.
- Docker runtime details map to `networking`, `directories`, `credentials`, `environment`, `resources`, and `labels`.
- A2 Registry/Square registration maps to `registry`; use `registry.enabled: true` to register the running local container and configure its inbound tunnel.

## Minimal Single Agent

```yaml
version: "1"
agent:
  name: code-reviewer
  image: a2gent-brute:latest
  host_port: 18080
  agent_kind: reviewer
  system_prompt: |
    You are a focused code reviewer. Prioritize correctness risks and actionable findings.
  project:
    id: project-app
    mount: ro
  tools:
    enabled: [read, grep, find_files]
```

## Batch Agents

```yaml
version: "1"
continue_on_error: true
defaults:
  image: a2gent-brute:latest
  name_prefix: session-agent
  start_port: 18100
  project:
    id: project-app
    mount: ro
  tools:
    enabled: [read, grep, find_files]
agents:
  - agent_kind: researcher
    system_prompt: Research options and cite tradeoffs.
  - agent_kind: planner
    system_prompt: Turn research into a small implementation plan.
  - agent_kind: reviewer
    system_prompt: Review the final diff for regressions.
```

## Docker Model Runner

```yaml
version: "1"
agent:
  name: local-dmr-reviewer
  image: a2gent-brute:latest
  host_port: 18080
  agent_kind: reviewer
  llm:
    provider: dmr
    model: ai/qwen3
    # Optional override. Defaults to the container OpenAI-compatible DMR URL.
    # base_url: http://model-runner.docker.internal/engines/v1
  networking:
    extra_hosts:
      - model-runner.docker.internal:host-gateway
  tools:
    enabled: [read, grep]
```

## Startup Prompt

`startup.prompt` creates a child Brute session in the new container. By default the session is queued with the prompt attached, so startup is quick and the user can inspect it before running. Set `auto_run: true` to immediately send the prompt to the new agent.

```yaml
version: "1"
agent:
  name: first-pass-reviewer
  image: a2gent-brute:latest
  agent_kind: reviewer
  startup:
    prompt: |
      Inspect the mounted workspace and summarize the first correctness risks.
    auto_run: false
```

## A2 Registry / Square registration

`registry.enabled: true` registers the created container with an A2 Registry/Square instance after Docker startup and writes the returned API key into the child container's `a2_registry` integration so it can connect back through the NAT tunnel.

For safety, YAML-driven registration defaults to `discoverable: false`; set `discoverable: true` only when you intentionally want a public listing. Parent agent credentials may auto-approve hidden/private child agents for tunnel connectivity, but public discoverability still remains a manual owner workflow.

If omitted, `owner_email`, `registry_url`, `transport`, `square_grpc_addr`, and `square_ws_url` are inherited from the parent Brute `a2_registry` integration/settings when available. When `owner_email` is missing but the parent integration has an API key, Brute also tries `GET /agents/me` against the registry to resolve it automatically.

```yaml
version: "1"
agent:
  name: seo-structure-auditor
  registry:
    enabled: true
    # owner_email and registry_url can be inherited from the parent a2_registry integration.
    # owner_email: owner@example.com
    # registry_url: https://a2gent.net
    agent_name: SEO Structure Auditor
    agent_handle: seo-structure-auditor
    description: Audits live websites and local HTML/Markdown for SEO, accessibility, performance, and AI/browser readability.
    network_access: behind_nat
    agent_type: personal
    category: marketing
    discoverable: false
    official_website: https://a2gent.net
    supports_audio: false
    supports_images: false
    supports_video: false
    price_per_session: 0.001
    currency: USD
```

## Fields

- `version`: only `1` is supported.
- `continue_on_error`: for batches, keep creating later agents after a failure. Defaults to true.
- `defaults`: any agent field applied to every `agents` item unless overridden.
- `agent`: one agent spec.
- `agents`: a list of agent specs for batch creation.
- `name`, `name_prefix`, `start_port`, `image`, `host_port`: container identity and port assignment.
  When `host_port` is omitted or non-positive, Brute auto-selects a host port from the local agent range and reserves it inside the parent process while `docker run` is in flight. If Docker still reports a port-allocation race, Brute retries with another auto-selected port before failing.
- `agent_kind`, `system_prompt`: Brute role label and system prompt.
- `initial_prompt` or `startup.prompt`: initial session prompt. Prefer `startup.prompt`.
- `startup.auto_run`: run the startup prompt immediately when true; otherwise create a queued child session.
- `registry.enabled`: register the created container in A2 Registry/Square and configure the child `a2_registry` tunnel integration.
- `registry.owner_email`, `registry.registry_url`, `registry.transport`, `registry.square_grpc_addr`, `registry.square_ws_url`: registry/tunnel connection settings; inherited from the parent `a2_registry` integration/settings when omitted.
- `registry.agent_name`, `registry.agent_handle` (or aliases `agent_id`/`public_id`), `registry.description`, `registry.category`, `registry.agent_type`, `registry.network_access`, `registry.official_website`: public registry metadata.
- `registry.discoverable`: public discoverability flag. YAML registration defaults to `false`; use `true` only with explicit intent and Square owner approval.
- `registry.supports_audio`, `registry.supports_images`, `registry.supports_video`, `registry.price_per_request`, `registry.price_per_input_kb`, `registry.price_per_output_kb`, `registry.price_per_session`, `registry.currency`: modality and pricing metadata.
- `session_id`: parent/source session label stored on the container and child startup session metadata.
- `project.id` / `project.mount`: mount a Caesar project into `/workspace` as `ro` or `rw`.
- `llm.provider`, `llm.model`, `llm.base_url`, `llm.lm_studio_base_url`: child agent provider/model routing.
- `tools.mode`: set to `all` to enable all tools.
- `tools.enabled`: allow only the listed Brute tools.
- `tools.disabled`: disable listed tools; can be combined with `mode: all`.
- `environment`: literal container environment variables.
- `credentials`: environment variables populated from `value`, host `env`, or a `file` path.
- `networking.network`, `networking.internet_access`, `networking.aliases`, `networking.extra_hosts`, `networking.publish`: Docker network options.
- `directories.data`: host data directory mounted as `/data`.
- `directories.workspace`: custom `/workspace` mount; do not combine it with `project`.
- `directories.volumes`: extra host mounts.
- `resources.cpus`, `resources.memory`, `resources.gpus`: Docker resource flags.
- `labels`: extra Docker labels.
