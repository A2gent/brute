# 🗡️ A²gent/brute terminal agent

[![Go Version](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Unit Tests](https://github.com/A2gent/brute/actions/workflows/tests.yml/badge.svg)](https://github.com/A2gent/brute/actions/workflows/tests.yml)
[![Coverage](https://codecov.io/gh/A2gent/brute/graph/badge.svg)](https://codecov.io/gh/A2gent/brute)

A Go-based autonomous AI coding agent with TUI + HTTP API.
Works best with [A²gent/caesar](https://github.com/A2gent/caesar) as control app.

<img width="1600" height="486" alt="Screenshot 2026-02-18 at 02 28 44" src="https://github.com/user-attachments/assets/829a71f2-e5c2-4258-8fbd-74071aa52dec" />
<img width="1415" height="483" alt="Screenshot 2026-02-16 at 01 01 04" src="https://github.com/user-attachments/assets/0b472db5-8a78-4f39-8e28-65d50211cc68" />

## 1. Quick Start

```bash
# one-line install from GitHub (main branch)
curl -fsSL https://raw.githubusercontent.com/A2gent/brute/main/install-from-github.sh | bash

brute
```

Then configure your provider inside the agent (`/provider`) or in the web app Providers page.

The installer builds from current source and installs:
- `brute` (primary CLI)

Manual install from a local clone is also supported:

```bash
git clone https://github.com/A2gent/brute.git
cd brute
./install.sh
```

### Dockerized quick start

Use Docker when you want to run the agent without installing the native CLI on your host. API/server mode is the default container workflow and is the best fit for Caesar/web-app integration.

```bash
git clone https://github.com/A2gent/brute.git
cd brute

# Start the API server on http://localhost:8080
docker compose up --build brute
```

For an interactive TUI inside Docker, run it from a real terminal:

```bash
docker compose down --remove-orphans
docker compose run --rm --build --service-ports -it brute-tui
```

If you use `just`, the same workflows are available as:

```bash
just docker-api      # API/server mode
just docker-tui      # interactive TUI mode
just docker-stop     # stop containers
```

Dockerized data is persisted on the host at `$HOME/.a2gent-data` and mounted into the container as `/data`. The current repository is mounted as `/workspace`, which is the project tree visible to the agent.

### Windows

The GitHub installer is a Bash installer. On Windows, use WSL2 (Ubuntu) or run the Docker image from a Docker Desktop setup with WSL integration enabled. Native PowerShell/CMD installation is not currently supported.

From PowerShell, install WSL once:

```powershell
wsl --install -d Ubuntu
```

Then open the Ubuntu terminal and run:

```bash
sudo apt update
sudo apt install -y curl tar git build-essential

# Install Go 1.24+ from https://go.dev/doc/install, then verify:
go version

curl -fsSL https://raw.githubusercontent.com/A2gent/brute/main/install-from-github.sh | bash

# If the installer says ~/.local/bin is not on PATH:
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc

brute
```

Windows notes:

- Brute data lives inside WSL by default at `~/.local/share/aagent` unless `AAGENT_DATA_PATH` is set.
- Keep projects inside the WSL home directory for best filesystem performance; Windows files are available under `/mnt/c/...` when needed.
- Browser, camera, and local audio integrations may need extra Windows/WSL setup and are less tested than macOS/Linux paths.
- For Docker on Windows, enable Docker Desktop WSL integration and run the Docker commands below from the Ubuntu shell.

## 2. Prerequisites

- Go 1.24+
- `just` command runner ([install](https://github.com/casey/just#installation))
- API key for at least one remote provider (unless you use local LM Studio)
- macOS camera features: Xcode Command Line Tools (`xcode-select --install`)
- For local audio in web-app (Whisper/Piper bootstrap):
  - `cmake`
  - `ffmpeg`
  - `pkg-config`
  - Install on macOS: `brew install cmake ffmpeg pkg-config`

## 3. Features

### 3.1 Core Agent Capabilities

- Comprehensive tool system:
- File operations: `read`, `write`, `edit`, `replace_lines`
- Search: `glob`, `grep`, `find_files`
- Execution: `bash` command execution
- Media: screenshot capture and camera photo capture
- Extensible architecture for custom/server-backed tools

### 3.2 Agentic Execution

- Agentic loop: task -> LLM with tools -> tool execution -> result feedback -> repeat
- A2A bridge support: canonical message endpoint + outbound tunnel-based chat + agent-card discovery

### 3.3 LLM Provider Support

- Multi-provider support: Anthropic Claude, Kimi, Google Gemini, LM Studio, OpenAI-compatible endpoints
- Auto-router and fallback chain support for reliability
- In-session provider/model switching support (web app flow)

Cursor Composer is launched through Cursor Agent CLI in headless `--print` mode. Because there is no interactive approval UI in this path, Brute passes `--force` by default so Cursor's native shell/edit tools can run; set `AAGENT_CURSOR_CLI_FORCE=false` to fall back to Cursor's allowlist prompts/config. Cursor API keys are passed through `CURSOR_API_KEY`, not command-line arguments.

### 3.4 Session and Persistence

- SQLite persistence for sessions, messages, jobs, integrations, and app settings
- Session resumption and parent/child session relationships
- Recurring jobs and project-aware session organization

### 3.5 TUI Experience

- Interactive terminal UI with status bar, model display, token/context metrics, and session timer
- Multi-line input and command palette behavior
- Live message stream with tool call/result rendering

### 3.6 HTTP API and Integrations

- REST API for web-app integration
- Session management endpoints (create/list/resume/manage)
- Speech and integration plumbing (including Whisper-related flows)

### 3.7 Reliability and Performance

- Lightweight runtime footprint
- Context window tracking and management
- Structured logging and practical failure handling

## Workflow Definition YAML Standard

Workflow files used by the web app are stored in the `system-soul` project under `workflows/*.yaml`.

Minimal structure:

```yaml
id: custom-workflow
name: Custom workflow
description: Optional summary
entryNodeId: user
policy:
  stopCondition: manual   # manual | max_turns | consensus | judge | timebox
  maxTurns: 12
  timeboxMinutes: 20
  # required when stopCondition: judge
  # judgeNodeId: critic
nodes:
  - id: user
    label: User           # optional; defaults to id
    kind: user            # optional; inferred when omitted
  - id: worker_a
    label: Research A
    kind: subagent
    ref: researcher-a     # sub-agent id/name (optional if label resolves)
  - id: critic
    label: Critic
    kind: subagent
    ref: critic-agent
edges:
  - from: user
    to: worker_a
  - from: worker_a
    to: critic
    mode: sequential      # optional; inferred as parallel for fan-out
```

Validation rules:
- `nodes[].id` is required and must be unique.
- `edges[].from` and `edges[].to` must reference existing node ids.
- `policy.stopCondition: judge` requires `policy.judgeNodeId`.
- `policy.judgeNodeId` must reference an existing node id.
- `kind` and `ref` are optional, but agent nodes should resolve to configured agents.
- Loops are supported (for example `developer -> critic -> developer`).
- For looped graphs, use `policy.maxTurns` and/or `policy.timeboxMinutes` to cap iterations.
- For `stopCondition: judge`, the judge node should emit `VERDICT: APPROVED` to end early; otherwise the loop continues until limits are reached.

## 4. Run Modes

### 4.1 Native (recommended for local TUI)

```bash
# install + run from any directory
brute

# build only
just build

# API only (uses the local app/extension default port 5445)
brute server

# force a custom API port when needed
brute --port 8080
```

By default, the embedded HTTP API binds to port `5445`, which is also the default Caesar and Chrome extension endpoint.  
Use `--port 0` only when you explicitly want the OS to choose a random free port; in that case the selected URL is printed on startup (for example: `HTTP API server running on http://0.0.0.0:49162`).

### 4.2 Docker

Build image:

```bash
docker build -t a2gent-brute:latest -f Dockerfile .
```

Run directly:

```bash
docker run --rm -it \
  --name a2gent-brute \
  --read-only \
  --tmpfs /tmp:exec,size=256m \
  -p 8080:8080 \
  -v "$PWD":/workspace \
  -v "$HOME/.a2gent-data":/data \
  a2gent-brute:latest server --port 8080
```

Run with compose helpers:

```bash
# API mode
just docker-api

# API mode with explicit LM Studio endpoint (useful for Tailscale IP)
just docker-api-lmstudio http://100.x.y.z:1234/v1

# interactive TUI mode (must run in a real terminal)
just docker-tui

# stop
just docker-api-down
# or
just docker-stop
```

Docker notes:

- `/workspace` is the agent-visible project tree.
- `/data` stores DB, logs, and config (`AAGENT_DATA_PATH=/data`).
- Runtime image is Alpine-based and includes `ffmpeg`.
- Default LM Studio URL in compose is `http://host.docker.internal:1234/v1`.
- For Tailscale-hosted LM Studio, prefer direct Tailscale IP over MagicDNS hostname.

### 4.3 Apple Container (macOS)

Requirement: install Apple container CLI: [github.com/apple/container](https://github.com/apple/container)

```bash
# build image
just apple-build

# API mode
just apple-api

# API mode with explicit LM Studio endpoint (recommended for Tailscale)
LM_STUDIO_BASE_URL=http://100.x.y.z:1234/v1 just apple-api

# interactive TUI mode
just apple-tui

# stop running brute containers
just apple-stop

# stop Apple container runtime VM
just apple-system-stop
```

## 5. Configuration

### 5.1 Config File

Canonical location (single-folder layout with DB/logs):

| Location | Scope |
|---|---|
| `$AAGENT_DATA_PATH/config.json` | user-level |

Defaults:

- `AAGENT_DATA_PATH=~/.local/share/aagent`
- config: `~/.local/share/aagent/config.json`
- database: `~/.local/share/aagent/aagent.db`
- logs: `~/.local/share/aagent/logs/`

Backward-compatible read fallbacks are still supported:

- `.aagent/config.json`
- `~/.config/aagent/config.json`

### 5.2 `.env` Loading

The app loads `.env` from:

- current directory
- `~/.env`

### 5.3 Environment Variables

Provider/API keys are usually configured inside the agent UI and persisted to local settings.
Environment variables are optional and mainly useful for headless/server workflows.

| Variable | Description |
|---|---|
| `ANTHROPIC_API_KEY` | Anthropic key (legacy/API-compatible paths only; the Anthropic provider uses Claude Code CLI login) |
| `KIMI_API_KEY` | Kimi key |
| `GEMINI_API_KEY` | Gemini key |
| `OPENAI_API_KEY` | OpenAI-compatible key |

Common optional variables:

| Variable | Default | Description |
|---|---|---|
| `AAGENT_PROVIDER` | `auto` | active provider (`anthropic`, `kimi`, `gemini`, `lmstudio`, `auto-router`) |
| `AAGENT_MODEL` | provider-specific | model override |
| `AAGENT_CLAUDE_CLI_PATH` | `claude` | Claude Code CLI executable used by the Anthropic provider |
| `AAGENT_CLAUDE_CLI_PERMISSION_MODE` | `acceptEdits` | Claude CLI permission mode for non-interactive runs; set to `default`, `auto`, or `bypassPermissions` if needed |
| `AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE` | `true` | disable Claude CLI session persistence for isolated A2gent turns |
| `AAGENT_CLAUDE_RATE_LIMITS_PATH` | `~/.a2gent/claude-rate-limits.json` | optional Claude Code statusLine cache file used to display last known Anthropic rate-limit usage |
| `AAGENT_CLAUDE_RATE_LIMITS_MAX_AGE` | `12h` | maximum allowed age for the Claude Code rate-limit cache before Brute shows it as stale |
| `KIMI_BASE_URL` | `https://api.kimi.com/coding/v1` | Kimi endpoint |
| `GEMINI_BASE_URL` | `https://generativelanguage.googleapis.com` | Gemini endpoint |
| `LM_STUDIO_BASE_URL` | `http://localhost:1234/v1` | LM Studio endpoint |
| `AAGENT_DATA_PATH` | `~/.local/share/aagent` | data directory |
| `AAGENT_FALLBACK_PROVIDERS` | - | fallback chain list |
| `A2GENT_TOOL_RESULT_COMPRESSION_ENABLED` | `true` | enable session-scoped compression of large LLM-bound tool results by default; compressed requests keep deterministic markers and allow `context_retrieve` lookup by hash while leaving exact `read`/`write`/`edit` outputs unchanged by default |

Speech transcription variables:

| Variable | Default | Description |
|---|---|---|
| `AAGENT_WHISPER_MODEL` | - | explicit local whisper.cpp model path; overrides model-name selection |
| `AAGENT_WHISPER_MODEL_NAME` | `small` | default auto-downloaded model alias or filename for normal voice input |
| `AAGENT_WHISPER_MEETING_MODEL_NAME` | `large-v3-turbo` | auto-downloaded model alias or filename for Caesar meeting transcription |
| `AAGENT_WHISPER_LANGUAGE` | `auto` | default language when the client does not provide one |
| `AAGENT_WHISPER_THREADS` | whisper.cpp default | thread count passed to `whisper-cli` |
| `AAGENT_WHISPER_NO_GPU` | `false` | force CPU-only whisper.cpp execution |

Supported model aliases include `tiny`, `base`, `small`, `medium`, `large-v3-turbo`, and `large-v3`, plus Superwhisper-style aliases `fast`, `nano`, `standard`, `pro`, `ultra-v3-turbo`, and `ultra`.

## 6. Common Commands

| Command | Description |
|---|---|
| `brute` | launch TUI |
| `brute "<task>"` | start with an initial task |
| `brute --continue <session-id>` | resume session |
| `brute session list` | list sessions |
| `brute logs` | show logs |
| `brute logs -f` | follow logs |
| `brute --port 8080` | run with fixed API port |

## A2A Support

Brute supports A2A communication in two modes:

- Protocol-style HTTP endpoint for inbound A2A messages
- Tunnel-backed outbound messaging to remote agents via Square

### Agent Card

- `GET /.well-known/agent-card.json`
- `supportedInterfaces[0].url` points to `/a2a/messages/send`

### Canonical A2A Endpoints (HTTP)

| Method | Path | Description |
|---|---|---|
| `POST` | `/a2a/messages/send` | Handle canonical A2A message (`content[]`) and return final response |
| `POST` | `/a2a/messages/send/stream` | SSE stream for inbound A2A message lifecycle (`accepted`, `running`, final `message`) |

### Outbound A2A Session Endpoints (Web-App Flow)

| Method | Path | Description |
|---|---|---|
| `POST` | `/a2a/outbound/sessions` | Create local outbound A2A session bound to target agent |
| `POST` | `/a2a/outbound/sessions/{sessionID}/chat` | Send outbound A2A message (sync) |
| `POST` | `/a2a/outbound/sessions/{sessionID}/chat/stream` | Send outbound A2A message (SSE progress stream) |

### Payload Format

- Canonical requests use `content[]` parts (`text`, `image_url`, `image_base64`)
- Image-only and text+image requests are supported
- For compatibility, brute still understands legacy bridge fields (`task`, `images`, `result`) used by existing tunnel/proxy flows

## 7. Session Model

- Sessions are persisted in a single SQLite DB (`AAGENT_DATA_PATH/aagent.db`).
- Session fields include `id`, `agent_id`, `title`, `status`, timestamps, optional `parent_id` and `job_id`.
- Grouping available now: parent/child sessions and job sessions.
- Not currently in HTTP session API: first-class project/folder filtering.

## 8. Database

DB path:

```bash
~/.local/share/aagent/aagent.db
# or
$AAGENT_DATA_PATH/aagent.db
```

Main tables:

- `sessions`
- `messages`
- `recurring_jobs`
- `job_executions`
- `app_settings`
- `integrations`
- `mcp_servers`
- `projects`

Quick query:

```bash
sqlite3 ~/.local/share/aagent/aagent.db
SELECT id, title, status, created_at FROM sessions ORDER BY created_at DESC LIMIT 10;
```

## 9. Development

```bash
just run         # run with go run
just dev         # API hot reload (air)
just build       # build binary
just test        # run tests
just fmt         # go fmt
just lint        # go vet
```

## 10. Testing

```bash
# all tests
just test

# unit tests (race-enabled)
just test-unit

# integration tests (separate pipeline)
just test-integration

# unit tests with coverage output
just test-coverage

# one package
go test -v ./internal/tools/...
```

## 11. Troubleshooting

### 11.1 API key missing

Configure a provider in-agent first (`/provider` in TUI or Providers in web app).
If you run headless, set one provider key via env:

```bash
export KIMI_API_KEY=...
# or GEMINI_API_KEY / OPENAI_API_KEY
```

For the Anthropic provider, install and log in to Claude Code CLI instead of configuring an API key; see 11.3.

### 11.2 Provider not available

```bash
export AAGENT_PROVIDER=auto-router
export AAGENT_FALLBACK_PROVIDERS=anthropic,kimi,gemini
```

### 11.3 Anthropic / Claude cannot edit files

The Anthropic provider intentionally uses the local Claude Code CLI (`claude`) and your existing Claude login, not the Anthropic API. Ensure Claude Code is installed and available on `PATH`, or set:

```bash
export AAGENT_CLAUDE_CLI_PATH=/path/to/claude
```

A2gent runs Claude CLI in non-interactive mode with native Claude tools enabled and defaults to `AAGENT_CLAUDE_CLI_PERMISSION_MODE=acceptEdits` so Sonnet can inspect and edit files without a hidden permission prompt. If your environment requires a different Claude permission policy, override that variable.

To display Anthropic usage in Caesar, Brute can read the last known Claude Code `statusLine` rate limits from a local JSON cache file. Brute does **not** launch an interactive Claude session or parse TUI output. Instead, configure a small Claude Code status line script that writes only the `rate_limits` object to disk whenever Claude updates the status line.

Example `~/.claude/statusline-rate-limits.py`:

```python
#!/usr/bin/env python3
import json
import os
import pathlib
import sys
import tempfile

cache_path = pathlib.Path(os.environ.get("AAGENT_CLAUDE_RATE_LIMITS_PATH", "~/.a2gent/claude-rate-limits.json")).expanduser()
cache_path.parent.mkdir(parents=True, exist_ok=True)

payload = json.load(sys.stdin)
rate_limits = payload.get("rate_limits") or {}
cache_payload = {"rate_limits": rate_limits}

with tempfile.NamedTemporaryFile("w", dir=cache_path.parent, delete=False) as tmp:
    json.dump(cache_payload, tmp)
    tmp.write("\n")
    temp_name = tmp.name

os.replace(temp_name, cache_path)
print("cc")
```

Make it executable and point Claude Code `statusLine.command` at it, for example:

```json
{
  "statusLine": {
    "command": "~/.claude/statusline-rate-limits.py"
  }
}
```

Notes:

- Claude Code provides `rate_limits.five_hour` and `rate_limits.seven_day` only for Claude.ai subscriber sessions after the first API response.
- Brute reads `AAGENT_CLAUDE_RATE_LIMITS_PATH` or defaults to `~/.a2gent/claude-rate-limits.json`.
- Brute treats the cache as stale after `AAGENT_CLAUDE_RATE_LIMITS_MAX_AGE` (default `12h`) and falls back to an unavailable message until a newer snapshot is written.
- Anthropic Docker sub-agents that use the parent LLM proxy mount this cache read-only. If a cached Claude window has `0%` left, the child `/health` endpoint returns `503` with `status: "offline"` until the cache shows usage available again.
- You can test the script with mock input from the Claude Code status line docs before enabling it in your real config.

### 11.4 TUI in container fails with `/dev/tty`

Run TUI from a real interactive terminal (`just docker-tui`) or use API mode (`just docker-api`).

### 11.5 LM Studio + Tailscale in container

Use Tailscale IP, not MagicDNS hostname, e.g.:

```bash
just docker-api-lmstudio http://100.x.y.z:1234/v1
```

## 12. Project Structure

```text
aagent/
├── cmd/aagent/         # CLI entry point
├── internal/
│   ├── agent/          # orchestrator and loop
│   ├── config/         # config management
│   ├── llm/            # provider clients
│   ├── logging/        # logs
│   ├── session/        # session model + manager
│   ├── storage/        # SQLite store
│   ├── tools/          # built-in tools
│   └── tui/            # Bubble Tea UI
├── justfile
└── README.md
```

## 13. Contributing

1. Fork the repository.
2. Create a branch (`git checkout -b feature/your-change`).
3. Commit and push changes.
4. Open a pull request.

## 14. License and Support

License: MIT

| Channel | Contact |
|---|---|
| Founder Telegram | `@tot_ra` |
| X / Twitter | `@tot_ra` |
| Schedule Demo | `https://calendly.com/artkurapov/30min` |
| Email | `artkurapov at gmail.com` |
