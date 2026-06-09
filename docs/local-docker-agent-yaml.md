# Local Docker Agent YAML

Reusable local Brute agents can be described in YAML and started through either Caesar's **Create Local Agent** view or the `create_local_docker_agents_from_yaml` tool. Store reusable configs in Soul under `agents/*.yaml`; with the default Brute data path that is `~/.local/share/aagent/agents/*.yaml`.

The launcher is intentionally a Brute container launcher, not a replacement for Docker Agent. It maps the useful Docker agentic concepts to Brute runtime knobs:

- Docker Agent-style model selection maps to `llm.provider`, `llm.model`, and `llm.base_url`.
- Docker Model Runner is supported with `llm.provider: dmr`; the container uses Brute's OpenAI-compatible provider and `http://model-runner.docker.internal/engines/v1`.
- Docker/MCP-style tool boundaries map to Brute's `tools.enabled`, `tools.disabled`, and `tools.mode`.
- Docker runtime details map to `networking`, `directories`, `credentials`, `environment`, `resources`, and `labels`.

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

## Fields

- `version`: only `1` is supported.
- `continue_on_error`: for batches, keep creating later agents after a failure. Defaults to true.
- `defaults`: any agent field applied to every `agents` item unless overridden.
- `agent`: one agent spec.
- `agents`: a list of agent specs for batch creation.
- `name`, `name_prefix`, `start_port`, `image`, `host_port`: container identity and port assignment.
- `agent_kind`, `system_prompt`: Brute role label and system prompt.
- `initial_prompt` or `startup.prompt`: initial session prompt. Prefer `startup.prompt`.
- `startup.auto_run`: run the startup prompt immediately when true; otherwise create a queued child session.
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
