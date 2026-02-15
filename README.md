# A² Brute / aagent

A Go-based autonomous AI coding agent that executes tasks in sessions.

## Features

- **TUI Interface**: Beautiful terminal UI with scrollable history, multi-line input, and real-time status
- **Agentic Loop**: Receive task → call LLM with tools → execute tool calls → return results → repeat until complete
- **Session Persistence**: SQLite-based session storage with resumption support
- **Session Relationships**: Supports parent/child sessions (`parent_id`) and recurring-job sessions (`job_id`)
- **Tool System**: Modular, extensible tools (bash, read, write, edit, glob, grep)
- **Kimi Code**: Uses Kimi Code API (Anthropic-compatible) as the LLM backend
- **Live Metrics**: Token usage tracking and context window percentage display
- **File Logging**: All operations logged to file for debugging

## Session Model (Important)

- Sessions are persisted in a single SQLite store (`AAGENT_DATA_PATH` / `config.data_path`).
- A session has `id`, `agent_id`, `title`, `status`, timestamps, and optional `parent_id` / `job_id`.
- Session metadata exists internally, but there is currently no first-class `project` or `folder` field in the HTTP session API.
- The HTTP `/sessions` list endpoint does not support grouping or filtering by project/folder today.

Current scope:
- Supported grouping: sub-sessions via `parent_id`, job-related sessions via `job_id`.
- Not currently implemented: project-based session grouping tied to filesystem folders in the frontend/API.

## Installation

```bash
# Build from source
just build

# Or install to GOPATH/bin
just install
```

## Usage

```bash
# Set your API key (or add to .env file)
export KIMI_API_KEY=sk-kimi-...

# Launch TUI (interactive mode)
aa

# Run with an initial task
aa "Create a hello world Go program"

# Resume a previous session
aa --continue <session-id>

# List sessions
aa session list

# View logs
aa logs

# Follow logs in real-time
aa logs -f
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

Configuration is loaded from:
1. `.aagent/config.json` (project-level)
2. `~/.config/aagent/config.json` (user-level)

Environment variables are loaded from `.env` files in:
- Current directory
- Home directory
- `~/git/mind/.env`

Environment variables:
- `KIMI_API_KEY` - Kimi Code API key (required)
- `ANTHROPIC_API_KEY` - Alternative to KIMI_API_KEY
- `OPENAI_API_KEY` - OpenAI API key (for `openai` provider)
- `ANTHROPIC_BASE_URL` - Override API endpoint (default: `https://api.kimi.com/coding/v1`)
- `AAGENT_MODEL` - Override default model (default: `kimi-for-coding`)
- `AAGENT_DATA_PATH` - Data storage directory
- `AAGENT_WHISPER_BIN` - Optional path to `whisper-cli` binary for speech-to-text (if omitted, backend can auto-build it on first STT request)
- `AAGENT_WHISPER_MODEL` - Path to Whisper model file (for example `ggml-base.bin` or faster `ggml-tiny.bin`)
- `AAGENT_WHISPER_LANGUAGE` - Optional default STT language (`auto`, `en`, `ru`, etc.)
- `AAGENT_WHISPER_TRANSLATE` - Optional default translation mode (`true` to translate to English, default `false` to keep original language)
- `AAGENT_WHISPER_THREADS` - Optional thread count for whisper transcription
- `AAGENT_WHISPER_AUTO_SETUP` - Enable/disable automatic `whisper-cli` build on demand (default: enabled)
- `AAGENT_WHISPER_AUTO_DOWNLOAD` - Enable/disable automatic Whisper model download on demand (default: enabled)
- `AAGENT_WHISPER_SOURCE` - Optional local path to whisper.cpp source root (contains `CMakeLists.txt`)

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

## License

MIT
