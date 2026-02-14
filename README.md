# aa (aagent)

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

## Tools

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands |
| `read` | Read file contents with line range support |
| `write` | Create or overwrite files |
| `edit` | String replacement edits in files |
| `glob` | Find files by pattern |
| `grep` | Search file contents with regex |

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

## License

MIT
