# 🗡️ A²gent/brute terminal agent

[![Go Version](https://img.shields.io/badge/go-1.21+-00ADD8.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A Go-based autonomous AI coding agent that executes tasks in sessions with a beautiful TUI interface.
Works best with [A²gent/caesar](https://github.com/A2gent/caesar) as control app.

> **Note:** Supports multiple LLM providers including Anthropic Claude, Kimi, Gemini, LM Studio, and custom endpoints. You will need an API key for your chosen provider.


<img width="1600" height="486" alt="Screenshot 2026-02-18 at 02 28 44" src="https://github.com/user-attachments/assets/829a71f2-e5c2-4258-8fbd-74071aa52dec" />
<img width="1415" height="483" alt="Screenshot 2026-02-16 at 01 01 04" src="https://github.com/user-attachments/assets/0b472db5-8a78-4f39-8e28-65d50211cc68" />


## Features

### Core Agent Capabilities
- **Agentic Loop**: Autonomous task execution with tool calling - receive task → call LLM with tools → execute tool calls → return results → repeat until complete
- **Multi-Provider Support**: Works with Anthropic Claude, Kimi, Google Gemini, LM Studio, and custom OpenAI-compatible endpoints
- **Auto-Router**: Intelligent provider fallback - automatically switches to backup providers on failure
- **Comprehensive Tool System**: 
  - File operations: read, write, edit, replace_lines
  - Search: glob (file patterns), grep (content search), find_files (with filters)
  - Execution: bash command execution
  - Media: screenshot capture, camera photo capture
  - Extensible architecture for custom tools

### Session Management
- **SQLite Persistence**: All sessions, messages, and metadata stored in SQLite database
- **Session Resumption**: Continue previous work exactly where you left off
- **Session Relationships**: Parent/child sessions and recurring-job sessions
- **Project Organization**: Group sessions by project
- **Recurring Jobs**: Schedule and execute automated tasks

### Beautiful TUI Interface
- **Modern Design**: Clean, minimal interface with ASCII art welcome screen
- **Real-time Status**: 
  - Model name display at center top
  - Token usage and context window percentage
  - Current working directory
  - Session timer
- **Enhanced Input Area**: 
  - Dark gray background with white text
  - Light blue left border
  - Multi-line support (Alt+Enter for new line)
- **Smart Layout**: Keyboard shortcuts on the right, path on the left
- **Live Metrics**: Real-time token tracking and memory usage

### HTTP API
- **Web Integration**: Full REST API for web-app integration
- **Voice Support**: Speech-to-text with Whisper integration
- **Notifications**: Push notifications support
- **Session Management**: Create, list, resume, and manage sessions via API

### Performance & Reliability
- **Lightweight**: Minimal memory footprint compared to alternatives. Uses ~10MB of RAM.
- **Context Management**: Automatic context window tracking and compaction
- **Error Recovery**: Graceful error handling and provider fallback
- **File Logging**: Detailed logging for debugging and monitoring

## Quick Start

```bash
# 1. Clone and build
git clone <repo-url>
cd aagent
just build

# 2. Set your API key (choose one provider)
export ANTHROPIC_API_KEY=sk-ant-...
# or
export KIMI_API_KEY=sk-kimi-...
# or
export GEMINI_API_KEY=...

# 3. Launch and start coding!
a2 "Create a hello world Go program"
```

## Session Model (Important)

- Sessions are persisted in a single SQLite store (`AAGENT_DATA_PATH` / `config.data_path`).
- A session has `id`, `agent_id`, `title`, `status`, timestamps, and optional `parent_id` / `job_id`.
- Session metadata exists internally, but there is currently no first-class `project` or `folder` field in the HTTP session API.
- The HTTP `/sessions` list endpoint does not support grouping or filtering by project/folder today.

Current scope:
- Supported grouping: sub-sessions via `parent_id`, job-related sessions via `job_id`.
- Not currently implemented: project-based session grouping tied to filesystem folders in the frontend/API.

## Installation

### Prerequisites

- **Go 1.21+** - [Download Go](https://golang.org/dl/)
- **just** (command runner) - `cargo install just` or [other install methods](https://github.com/casey/just#installation)
- **API Key** - Choose your provider:
  - [Anthropic Claude](https://console.anthropic.com/) (recommended)
  - [Kimi](https://kimi.com)
  - [Google Gemini](https://ai.google.dev/)
  - [LM Studio](https://lmstudio.ai/) (local models)
  - Any OpenAI-compatible endpoint

### Build from Source

```bash
# Clone the repository
git clone <repo-url>
cd aagent

# Build binary
just build

# Install to GOPATH/bin
just install
```

## Usage

### Environment Setup

Set your API key for your chosen provider (or add to `.env` file in your project or home directory):

```bash
# Anthropic Claude (recommended)
export ANTHROPIC_API_KEY=sk-ant-...

# Or Kimi
export KIMI_API_KEY=sk-kimi-...

# Or Google Gemini
export GEMINI_API_KEY=...

# Or LM Studio (local)
export LM_STUDIO_BASE_URL=http://localhost:1234/v1
```

### Common Commands

| Command | Description |
|---------|-------------|
| `a2` | Launch interactive TUI mode |
| `a2 "<task>"` | Run with an initial task |
| `a2 --continue <session-id>` | Resume a previous session |
| `a2 session list` | List all sessions |
| `a2 logs` | View session logs |
| `a2 logs -f` | Follow logs in real-time |

### Examples

```bash
# Interactive mode
a2

# Run a specific task
a2 "Refactor the auth module to use JWT tokens"

# Continue previous work
a2 session list                    # Find your session ID
a2 --continue abc123-def456-789   # Resume from where you left off
```

## TUI Interface

The TUI provides a beautiful, modern interactive interface with:

- **Top Bar**: 
  - Left: Session title and ID
  - Center: Currently selected model (⚡ model-name)
  - Right: Token usage, context percentage, memory usage, timer, and status
- **Welcome Screen**: ASCII art "A² BRUTE" logo with sword when starting fresh
- **Message History**: Scrollable view of all conversation messages with color-coded tool calls and timestamps
- **Input Area**: 
  - Dark gray background with white text
  - Light blue left border (│) for visual distinction
  - Multi-line support (3 lines, Alt+Enter for new line)
- **Bottom Bar**:
  - Left: Current working directory path
  - Right: Context-aware keyboard shortcuts
- **Keyboard Shortcuts**:
  - `esc`: Quit
  - `enter`: Send message
  - `alt+enter`: Insert new line in input
  - `ctrl+c`: Cancel current operation (or force quit)
  - `/`: Open command menu

## Configuration

### Config Files

Configuration is loaded in order (later overrides earlier):

| Location | Scope |
|----------|-------|
| `.aagent/config.json` | Project-level |
| `~/.config/aagent/config.json` | User-level |

### Environment Files

`.env` files are loaded from:
- Current directory
- Home directory (`~/.env`)

### Environment Variables

#### Required (choose one)

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic Claude API key (recommended) |
| `KIMI_API_KEY` | Kimi API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `OPENAI_API_KEY` | OpenAI API key (or compatible) |

#### Optional

| Variable | Default | Description |
|----------|---------|-------------|
| `AAGENT_PROVIDER` | `auto` | LLM provider: `anthropic`, `kimi`, `gemini`, `lmstudio`, `auto-router` |
| `AAGENT_MODEL` | Provider-specific | Model name (e.g., `claude-sonnet-4`, `kimi-for-coding`) |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Anthropic API endpoint |
| `KIMI_BASE_URL` | `https://api.kimi.com/coding/v1` | Kimi API endpoint |
| `GEMINI_BASE_URL` | `https://generativelanguage.googleapis.com` | Gemini API endpoint |
| `LM_STUDIO_BASE_URL` | `http://localhost:1234/v1` | LM Studio endpoint |
| `AAGENT_DATA_PATH` | `~/.local/share/aagent` | Data storage directory |
| `AAGENT_FALLBACK_PROVIDERS` | - | Comma-separated list of fallback providers |

#### Speech-to-Text (Whisper)

| Variable | Description |
|----------|-------------|
| `AAGENT_WHISPER_BIN` | Path to `whisper-cli` binary |
| `AAGENT_WHISPER_MODEL` | Model file (e.g., `ggml-base.bin`) |
| `AAGENT_WHISPER_LANGUAGE` | STT language: `auto`, `en`, `ru`, etc. |
| `AAGENT_WHISPER_TRANSLATE` | `true` to translate to English |
| `AAGENT_WHISPER_THREADS` | Thread count for transcription |
| `AAGENT_WHISPER_AUTO_SETUP` | Auto-build whisper-cli (default: enabled) |
| `AAGENT_WHISPER_AUTO_DOWNLOAD` | Auto-download model (default: enabled) |
| `AAGENT_WHISPER_SOURCE` | Path to whisper.cpp source |

## Database

The agent uses SQLite for persistent storage of sessions, messages, recurring jobs, and settings.

### Database Location

The SQLite database is stored at:

```
~/.local/share/aagent/aagent.db
```

Or if `AAGENT_DATA_PATH` is set:

```
$AAGENT_DATA_PATH/aagent.db
```

### Database Schema

The database contains the following main tables:

- **sessions** - Session metadata (id, agent_id, parent_id, job_id, project_id, title, status, timestamps)
- **messages** - Conversation messages with tool calls and results
- **recurring_jobs** - Scheduled recurring tasks
- **job_executions** - Execution history for recurring jobs
- **app_settings** - Application settings and API keys
- **integrations** - External integrations (Telegram, Slack, etc.)
- **mcp_servers** - MCP server registry
- **projects** - Project groupings for sessions

### Accessing the Database

You can query the database directly using `sqlite3`:

```bash
sqlite3 ~/.local/share/aagent/aagent.db

# Example queries
SELECT id, title, status, created_at FROM sessions ORDER BY created_at DESC LIMIT 10;
SELECT COUNT(*) FROM messages WHERE session_id = 'your-session-id';
```

## Tools

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands |
| `read` | Read file contents with line range support |
| `write` | Create or overwrite files |
| `edit` | String replacement edits in files |
| `replace_lines` | Replace exact line ranges in files |
| `glob` | Find files by pattern |
| `find_files` | Find files with include/exclude filters |
| `grep` | Search file contents with regex |
| `take_screenshot_tool` | Capture screenshots (main/all/specific display/area) with configurable output path and Tools UI defaults |
| `take_camera_photo_tool` | Capture camera photos with configurable camera index/output path and optional inline image metadata for multimodal handoff (macOS uses native AVFoundation via cgo) |

## Project Structure

```
aagent/
├── cmd/aagent/         # CLI entry point
├── internal/
│   ├── agent/          # Agent orchestrator and loop
│   ├── config/         # Configuration management
│   ├── llm/            # LLM client interfaces
│   │   ├── anthropic/  # Anthropic/Kimi Code implementation
│   │   └── kimi/       # Kimi K2.5 (OpenAI-compatible, legacy)
│   ├── logging/        # File-based logging
│   ├── session/        # Session management
│   ├── storage/        # SQLite persistence
│   ├── tools/          # Tool implementations
│   └── tui/            # Terminal user interface (Bubble Tea)
├── go.mod
├── justfile
└── README.md
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Development

```bash
# Run directly (faster for development)
just run

# Run backend API server only
just server

# Install hot-reload tool once
just install-air

# Hot reload backend API server (restarts only after successful build)
just dev

# Build
just build

# Run tests
just test

# Format code
just fmt

# Lint
just lint

# View logs
just logs

# Follow logs
just logs-follow
```

Hot reload details:
- Uses `air` with project config at `.air.toml`.
- `stop_on_error = false` keeps the previous healthy process running when a code change fails to compile.
- The server restarts only after a successful `go build`, which avoids replacing a working backend with a broken one during self-edits.

## Troubleshooting

### API Key Issues

**Error:** API key not set

**Solution:** 
```bash
# Set the appropriate API key for your provider
export ANTHROPIC_API_KEY=sk-ant-your-key-here
# or
export KIMI_API_KEY=sk-kimi-your-key-here
# or
export GEMINI_API_KEY=your-key-here
```
Or create a `.env` file in your project directory with your chosen provider's key.

### Provider Issues

**Error:** Provider not available

**Solution:** Use auto-router for automatic fallback:
```bash
export AAGENT_PROVIDER=auto-router
export AAGENT_FALLBACK_PROVIDERS=anthropic,kimi,gemini
```

### Build Issues

**Error:** `command not found: just`

**Solution:** Install `just` command runner:
```bash
cargo install just
```

**Error:** Build fails with Go version error

**Solution:** Ensure you have Go 1.21+ installed:
```bash
go version
```

### Session Issues

**Error:** Cannot resume session

**Solution:** List available sessions and check the ID:
```bash
a2 session list
```

### Logs

View detailed logs for debugging:
```bash
a2 logs -f
```

## License

MIT
