# 🗡️ A²gent/brute terminal agent

[![Go Version](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A Go-based autonomous AI coding agent with TUI + HTTP API.
Works best with [A²gent/caesar](https://github.com/A2gent/caesar) as control app.

<img width="1600" height="486" alt="Screenshot 2026-02-18 at 02 28 44" src="https://github.com/user-attachments/assets/829a71f2-e5c2-4258-8fbd-74071aa52dec" />
<img width="1415" height="483" alt="Screenshot 2026-02-16 at 01 01 04" src="https://github.com/user-attachments/assets/0b472db5-8a78-4f39-8e28-65d50211cc68" />

## 1. Features

### 1.1 Core Agent Capabilities

- Comprehensive tool system:
- File operations: `read`, `write`, `edit`, `replace_lines`
- Search: `glob`, `grep`, `find_files`
- Execution: `bash` command execution
- Media: screenshot capture and camera photo capture
- Extensible architecture for custom/server-backed tools

### 1.2 Agentic Execution

- Agentic loop: task -> LLM with tools -> tool execution -> result feedback -> repeat
- A2A basic protocol support via agent-card endpoint

### 1.3 LLM Provider Support

- Multi-provider support: Anthropic Claude, Kimi, Google Gemini, LM Studio, OpenAI-compatible endpoints
- Auto-router and fallback chain support for reliability
- In-session provider/model switching support (web app flow)

### 1.4 Session and Persistence

- SQLite persistence for sessions, messages, jobs, integrations, and app settings
- Session resumption and parent/child session relationships
- Recurring jobs and project-aware session organization

### 1.5 TUI Experience

- Interactive terminal UI with status bar, model display, token/context metrics, and session timer
- Multi-line input and command palette behavior
- Live message stream with tool call/result rendering

### 1.6 HTTP API and Integrations

- REST API for web-app integration
- Session management endpoints (create/list/resume/manage)
- Speech and integration plumbing (including Whisper-related flows)

### 1.7 Reliability and Performance

- Lightweight runtime footprint
- Context window tracking and management
- Structured logging and practical failure handling

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

## 3. Quick Start

```bash
git clone https://github.com/A2gent/brute.git
cd brute

# pick one provider key
export ANTHROPIC_API_KEY=sk-ant-...
# or
export KIMI_API_KEY=sk-kimi-...
# or
export GEMINI_API_KEY=...

just start
```

## 4. Run Modes

### 4.1 Native (recommended for local TUI)

```bash
# build + run (TUI + API)
just start

# build only
just build

# API only
just server
```

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
  a2gent-brute:latest
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

Required (choose one for remote providers):

| Variable | Description |
|---|---|
| `ANTHROPIC_API_KEY` | Anthropic key |
| `KIMI_API_KEY` | Kimi key |
| `GEMINI_API_KEY` | Gemini key |
| `OPENAI_API_KEY` | OpenAI-compatible key |

Common optional variables:

| Variable | Default | Description |
|---|---|---|
| `AAGENT_PROVIDER` | `auto` | active provider (`anthropic`, `kimi`, `gemini`, `lmstudio`, `auto-router`) |
| `AAGENT_MODEL` | provider-specific | model override |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Anthropic endpoint |
| `KIMI_BASE_URL` | `https://api.kimi.com/coding/v1` | Kimi endpoint |
| `GEMINI_BASE_URL` | `https://generativelanguage.googleapis.com` | Gemini endpoint |
| `LM_STUDIO_BASE_URL` | `http://localhost:1234/v1` | LM Studio endpoint |
| `AAGENT_DATA_PATH` | `~/.local/share/aagent` | data directory |
| `AAGENT_FALLBACK_PROVIDERS` | - | fallback chain list |

## 6. Common Commands

| Command | Description |
|---|---|
| `a2` | launch TUI |
| `a2 "<task>"` | start with an initial task |
| `a2 --continue <session-id>` | resume session |
| `a2 session list` | list sessions |
| `a2 logs` | show logs |
| `a2 logs -f` | follow logs |

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

# with race + coverage
go test -v -race -coverprofile=coverage.out ./...

# one package
go test -v ./internal/tools/...
```

## 11. Troubleshooting

### 11.1 API key missing

Set one provider key:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
# or KIMI_API_KEY / GEMINI_API_KEY / OPENAI_API_KEY
```

### 11.2 Provider not available

```bash
export AAGENT_PROVIDER=auto-router
export AAGENT_FALLBACK_PROVIDERS=anthropic,kimi,gemini
```

### 11.3 TUI in container fails with `/dev/tty`

Run TUI from a real interactive terminal (`just docker-tui`) or use API mode (`just docker-api`).

### 11.4 LM Studio + Tailscale in container

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
