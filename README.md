# A² Brute terminal agent

[![Go Version](https://img.shields.io/badge/go-1.21+-00ADD8.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A Go-based autonomous AI coding agent that executes tasks in sessions with a beautiful TUI interface.
As opposed to [OpenCode](https://github.com/anomalyco/opencode), it does not consume hundreds MB of RAM.

Recommended to use [web-app](https://github.com/A2gent/web-app) for full experience (with voice and notifications).

> **Note:** This project uses the Kimi Code API (Anthropic-compatible) as its LLM backend. You will need an API key to use it.

<img width="1415" height="483" alt="Screenshot 2026-02-16 at 01 01 04" src="https://github.com/user-attachments/assets/0b472db5-8a78-4f39-8e28-65d50211cc68" />


## Features
- **Exposes HTTP API** for web-app to interact with
- **TUI Interface**: Beautiful terminal UI with scrollable history, multi-line input, and real-time status
- **Agentic Loop**: Receive task → call LLM with tools → execute tool calls → return results → repeat until complete
- **Session Persistence**: SQLite-based session storage with resumption support
- **Session Relationships**: Supports parent/child sessions (`parent_id`) and recurring-job sessions (`job_id`)
- **Tool System**: Modular, extensible tools (bash, read, write, edit, glob, grep)
- **Kimi Code**: Uses Kimi Code API (Anthropic-compatible) as the LLM backend
- **Live Metrics**: Token usage tracking and context window percentage display
- **File Logging**: All operations logged to file for debugging

## Quick Start

```bash
# 1. Clone and build
git clone <repo-url>
cd aagent
just build

# 2. Set your API key
export KIMI_API_KEY=sk-kimi-...

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
- **API Key** - Get your Kimi Code API key from [kimi.com](https://kimi.com)

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

Set your API key (or add to `.env` file in your project or home directory):

```bash
export KIMI_API_KEY=sk-kimi-...
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

The TUI provides an interactive interface with:

- **Top Bar**: Task summary on the left, token usage and context window percentage on the right
- **Message History**: Scrollable view of all conversation messages with timestamps
- **Status Line**: Loading indicator when processing, human-readable timer showing time since last input
- **Input Area**: Multi-line text area for entering queries (Alt+Enter for new line, Enter to send)
- **Keyboard Shortcuts**:
  - `esc`: Quit
  - `enter`: Send message
  - `alt+enter`: Insert new line in input
  - `ctrl+c`: Force quit

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
- `~/git/mind/.env`

### Environment Variables

#### Required

| Variable | Description |
|----------|-------------|
| `KIMI_API_KEY` | Kimi Code API key |
| `ANTHROPIC_API_KEY` | Alternative to KIMI_API_KEY |

#### Optional

| Variable | Default | Description |
|----------|---------|-------------|
| `ANTHROPIC_BASE_URL` | `https://api.kimi.com/coding/v1` | API endpoint |
| `AAGENT_MODEL` | `kimi-for-coding` | Default model |
| `AAGENT_DATA_PATH` | - | Data storage directory |

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

**Error:** `KIMI_API_KEY not set`

**Solution:** 
```bash
export KIMI_API_KEY=sk-kimi-your-key-here
```
Or create a `.env` file in your project directory with:
```
KIMI_API_KEY=sk-kimi-your-key-here
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
