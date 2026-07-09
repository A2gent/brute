# Terminal Emulation Tool (TUI Control)

## Idea

Give the Brute agent a way to launch an interactive terminal program — `vim`,
`htop`, `lazygit`, `psql`, an `ssh` session, a curses installer, a REPL — inside
a pseudo-terminal (PTY) and drive it the way a human at a keyboard would: send
keystrokes, watch the screen repaint, react to what it shows, and repeat.

The existing `bash` and `code_execution` tools are *batch* tools. They run a
command to completion and hand back captured stdout/stderr. That model breaks
down for programs that:

- take over the whole screen with an alternate-screen buffer (`vim`, `htop`);
- redraw in place using cursor movement and ANSI escapes;
- block waiting for interactive keypresses (`y/n` prompts, pagers, menus);
- only behave "normally" when they believe a real terminal (a TTY) is attached
  — many CLIs disable colors, prompts, or line editing when stdout is a pipe.

A terminal-emulation tool closes that gap. Conceptually it is the terminal
counterpart of the existing `browser_chrome` tool: `browser_chrome` drives a GUI
app (Chrome) through a persistent session with `navigate / type / click /
screenshot`; the terminal tool drives a **TUI app** through a persistent session
with `start / send_keys / read_screen`. This document reuses that tool's proven
shape deliberately.

## How it fits the current architecture

Tools implement a small interface and register with `tools.Manager`
(`internal/tools/manager.go`):

```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]interface{}
    Execute(ctx context.Context, params json.RawMessage) (*Result, error)
}
```

Two existing patterns carry almost the whole design:

1. **`browser_chrome`** (`internal/tools/integrationtools/browser_chrome*.go`)
   holds a long-lived resource (a `*rod.Browser` / `*rod.Page`) behind a
   `sync.Mutex`, exposes an **action-based** schema (`{"action": "...", ...}`),
   lazily starts the resource with an `ensureBrowser()` guard, verifies the
   handle is still alive on each call, and reconnects if it went stale. The
   terminal tool follows the same lifecycle, with a PTY + emulator in place of
   the browser + page.

2. **`ProgressCallback`** (`manager.go`) lets a long-running tool stream interim
   status into the chat without committing a final tool result. A TUI that takes
   seconds to settle (a build inside `lazygit`, a long `psql` query) can emit
   screen snapshots as progress while it waits.

Registration mirrors `browser_chrome`: add `NewTerminalTool(workDir)` to
`internal/tools/integrationtools/register.go` (or to `NewManager` in
`manager.go` if we want it always-on rather than integration-gated).

## The core mechanism

Three layers:

```
        keystrokes ──▶ ┌───────────┐  master fd  ┌──────────────┐
  agent send_keys      │    PTY    │◀───────────▶│  child proc  │  (vim/htop/…)
        read_screen ◀─ │  master   │  slave fd   │  thinks it   │
                       └─────┬─────┘             │  owns a tty  │
                             │ raw bytes          └──────────────┘
                             ▼
                     ┌───────────────┐
                     │  VT emulator  │  parses ANSI/CSI, maintains an
                     │ (screen grid) │  80x24 (configurable) cell buffer
                     └───────┬───────┘
                             ▼
                   plain-text snapshot for the LLM
```

1. **PTY** — allocate a pseudo-terminal and start the target program attached to
   the slave side. The program calls `isatty()` and believes a human is present,
   so it enables full interactive behavior. We read/write the master side.

2. **VT emulator** — the master side emits a raw byte stream full of escape
   sequences (`ESC[2J`, cursor moves, SGR color, alt-screen switches). We cannot
   show that to the model. A terminal emulator parses the stream and maintains an
   in-memory screen grid (rows × cols of cells), exactly like the visible region
   of a real terminal. This is what makes `htop` legible instead of garbage.

3. **Text projection** — flatten the current grid to plain text (optionally with
   the cursor position, and optionally a compact style map) and return that as
   the tool `Output`. This is the terminal analog of `browser_chrome`'s
   `get_text` / `screenshot`.

### Why a real emulator and not "just read the pipe"

Reading raw PTY bytes and stripping escape codes does **not** work for
full-screen apps. `htop` never prints "lines" — it positions the cursor and
overwrites cells in place. Without a grid model you get an unreadable soup of
control sequences. The emulator is the non-negotiable core of this tool.

## Library choices (Go) — minimizing dependencies

The project is Go 1.25 and already depends on the Charm/`bubbletea` ecosystem,
so ANSI tooling is partly in the tree already (`go.sum` shows
`charmbracelet/x/ansi`, `charmbracelet/x/cellbuf`, `charmbracelet/x/term`, plus
`x/sys v0.45.0`, `uniseg`, `go-colorful`, `runewidth`).

**Verified dependency cost** (from each candidate's published `go.mod`):

| Layer | Candidate | New modules added |
|-------|-----------|-------------------|
| PTY | `github.com/creack/pty` v1.1.24 | **1, zero transitive** — empty `require`, pure `syscall`/stdlib |
| VT emulator | `github.com/hinshun/vt10x` | **1, zero transitive** — empty `require`, pure stdlib |
| VT emulator (alt) | `github.com/charmbracelet/x/vt` | **~5+ new** — rebuilt on `charmbracelet/ultraviolet` (a rendering engine with its own tree) plus `x/exp/ordered`, `x/termios`, `x/windows`, … |
| Key encoding | *hand-rolled* (~40-line map) or `charmbracelet/x/ansi` (already present) | 0 |

**Recommended minimal stack: `creack/pty` + `hinshun/vt10x` = 2 new modules,
zero new transitive dependencies.** Everything else these two need is already
compiled into the binary. Do the human-notation → control-byte mapping by hand
(a small table; see the key-encoding section) so it needs no library at all.

Note the reversal from a first instinct: `charmbracelet/x/vt` looks like the
"consistent with our existing Charm deps" pick, but because it was rebuilt on
`charmbracelet/ultraviolet` it is now the **heaviest** option, not the cheapest.
The shared `x/ansi`/`x/cellbuf` packages come in either way; `ultraviolet` and
the termios/windows/exp modules are pure new cost.

Trade-off to accept: `hinshun/vt10x` is **unmaintained** (last release 2022,
`go 1.14`) but stable, tiny, and correct for VT100/xterm — enough to render
`vim`, `htop`, `less`, `psql` into a plain-text grid, which is all we project.
Choose `x/vt` only if we later need semantic color/style or hit an app `vt10x`
renders wrong; treat it as an upgrade path, not the default.

Absolute floor (1 new module): vendor the classic ~100-line PTY open
(`posix_openpt`/`grantpt`/`unlockpt` + `TIOCSWINSZ` via `x/sys/unix`, already in
the tree) instead of `creack/pty`. Not recommended — `creack/pty` is exactly
that code, battle-tested, and already zero-dependency, so vendoring buys nothing.

`os/exec` + `creack/pty` covers spawning. Resize uses `pty.Setsize`. No cgo.

## Tool API

Action-based schema, matching `browser_chrome` so the model reuses a familiar
mental model. Single tool, `terminal`, holding one (or a small pool of) session.

```jsonc
{
  "action": "start | send_keys | read_screen | wait_for | resize | interrupt | stop | list",
  "command": "vim notes.md",   // start: program + args (run via shell)
  "keys":    "iHello<Esc>:wq<Enter>", // send_keys: text + <Named> keys
  "text":    "raw literal text",       // send_keys alt: never interpret <...>
  "pattern": "Saved",                  // wait_for: substring/regex to appear
  "timeout_ms": 5000,                  // wait_for / read_screen settle window
  "cols": 120, "rows": 40,             // start / resize: grid size
  "session": "default"                 // optional: name a session for parallel TUIs
}
```

### Actions

- **start** — allocate a PTY (`cols`×`rows`, default 80×24), launch `command`
  through `bash -lc` in `workDir`, attach an emulator, begin draining the master
  fd into the grid on a background goroutine. Returns the initial screen once it
  settles (see synchronization). Idempotent per session: starting an occupied
  session errors unless `stop` was called.
- **send_keys** — encode `keys` (interpreting `<Enter>`, `<Esc>`, `<Tab>`,
  `<C-c>`, `<Down>`, `<F5>`, `<BS>` …) to bytes, write to the master fd, wait for
  the screen to settle, return the new screen. `text` sends literal bytes with no
  interpretation (for content that contains `<`).
- **read_screen** — return the current grid as text without sending input.
  Optional `timeout_ms` to first wait for the screen to go quiet.
- **wait_for** — block (up to `timeout_ms`) until `pattern` appears anywhere on
  the grid; return the screen and whether it matched. Essential for scripts that
  must wait for "Saved", a shell prompt, or a menu to render.
- **resize** — `pty.Setsize` + tell the emulator; apps receive `SIGWINCH` and
  repaint.
- **interrupt** — send `C-c` (`0x03`); convenience over `send_keys`.
- **stop** — send `SIGTERM` (then `SIGKILL` after a grace period), close the fd,
  drop the emulator, free the session.
- **list** — report active sessions, their command, size, alive/exited state,
  and last exit code.

### What the model sees (Output)

`read_screen` and friends return the flattened grid, e.g.:

```
┌ screen 80x24  cursor (5,12)  pid 40318  alt-screen ─────────────
 1  # notes.md
 2
 3  Hello world
 4  ~
 5  ~
...
24  "notes.md" 3L, 12B written
└──────────────────────────────────────────────────────────────
```

Row numbers and the header are tool chrome, not part of the emulated content;
they help the model reason about coordinates. A `style_map` or ANSI-preserving
mode can be offered behind a flag for cases where color carries meaning (e.g.
`htop`'s red/green), but plain text is the default because it is cheapest and
most legible for the model — the same reasoning `browser_chrome` uses in
preferring `get_text` over `screenshot`.

## Synchronization: the hard part

Unlike a batch command, a TUI has **no "done" signal** after a keystroke. Press
`j` in `vim` and the screen repaints in microseconds; run `:%!sort` and it takes
longer. The tool must decide when the screen has "settled" before answering, or
the model reads a half-drawn frame.

Strategy (quiet-period debounce, the standard `expect`/`tmux capture` approach):

1. After writing input, watch the byte stream from the master fd.
2. Reset a timer every time new bytes arrive.
3. Consider the screen settled when **no bytes arrive for `quiet_ms`** (default
   ~120ms) **or** a hard `timeout_ms` cap is hit (default a few seconds).
4. For deterministic waits, prefer `wait_for` with an explicit `pattern` — it
   returns the instant the target text renders, rather than guessing with timers.

Continuously-updating apps (`htop`, `top`, a progress spinner) never go quiet.
For those, `read_screen` returns the current frame after `timeout_ms` regardless,
and the `wait_for` pattern approach is how you catch a specific state. The
`ProgressCallback` can stream intermediate frames so the model/user see motion
during a long wait.

## Session lifecycle

Model directly on `browser_chrome`'s lifecycle:

```go
type TerminalTool struct {
    mu       sync.Mutex
    sessions map[string]*ptySession   // keyed by session name
    workDir  string
}

type ptySession struct {
    cmd    *exec.Cmd
    ptmx   *os.File          // creack/pty master
    term   *vt.Terminal      // emulator holding the grid
    mu     sync.Mutex        // serialize writes + reads
    lastByteAt atomic.Int64  // for quiet-period detection
    exited chan struct{}
    exitErr error
}
```

- **Lazy start / liveness check** — like `ensureBrowser()`, each call verifies
  the session's process is still alive; if it exited, report the exit code and
  final screen instead of writing into a dead fd.
- **Serialization** — a per-session mutex; the tool description must warn (as
  `browser_chrome` does) *not* to call it via `parallel` or issue multiple
  terminal calls in one turn against the same session. Distinct `session` names
  may run concurrent TUIs.
- **Cleanup** — register process teardown on agent shutdown so no orphan PTYs or
  child processes leak. `stop` on `SIGTERM`→grace→`SIGKILL`. Cap total live
  sessions.
- **Reaping** — a background goroutine drains the master fd into the emulator and
  closes `exited` on `cmd.Wait()`; store the exit error for `list`/`read_screen`.

## Key encoding reference

`send_keys` needs a human-friendly notation mapped to control bytes:

| Notation | Bytes | |
|----------|-------|--|
| plain text | UTF-8 as typed | |
| `<Enter>` / `<CR>` | `\r` (0x0D) | most TUIs want CR, not LF |
| `<Esc>` | `\x1b` | |
| `<Tab>` | `\t` | |
| `<BS>` | `\x7f` | |
| `<Space>` | `0x20` | |
| `<C-x>` | `x & 0x1f` | e.g. `<C-c>`=0x03, `<C-d>`=0x04 |
| `<Up>/<Down>/<Left>/<Right>` | `ESC[A/B/C/D` | cursor keys (normal mode) |
| `<Home>/<End>/<PgUp>/<PgDn>` | `ESC[H` / `ESC[F` / `ESC[5~` / `ESC[6~` | |
| `<F1>..<F12>` | `ESC O P` … / `ESC[..~` | |
| `<M-x>` | `ESC` + `x` | Alt/Meta |

Gotchas to bake in and document:
- **Enter is `\r`, not `\n`** for most curses apps.
- Cursor-key bytes differ in **application keypad mode** (`ESCOA` vs `ESC[A`);
  the emulator tracks the mode, so ideally the encoder consults it.
- Send **one logical action per call** and re-read the screen; blindly chaining
  many keystrokes ("`iHello<Esc>:wq<Enter>`") works for scripted flows but hides
  intermediate state from the model.

## Safety and limits

- **Timeouts** everywhere: settle timeout, `wait_for` timeout, per-call hard cap,
  and a max session lifetime.
- **Session cap** (e.g. ≤ 4 live PTYs) and grid-size cap to bound memory.
- **Bytes-per-second / total-bytes guard** on the master drain to survive an app
  that spews output forever; truncate the emulator input, never the grid.
- **Confirmed teardown** — `stop`/shutdown must actually kill the child (process
  group kill), or `ssh`/`vim` sessions leak.
- **Sandbox inheritance** — the child inherits the agent's shell environment and
  filesystem access; it is exactly as powerful as `bash`. No new privilege, but
  document that it can run editors, shells, `ssh`, etc., interactively.
- **Secrets** — interactive password prompts will render into the grid; the
  snapshot may capture typed passwords. Offer a `text` send that is excluded from
  logs, and scrub obvious prompt patterns from progress events.

## Implementation sketch

```go
// internal/tools/integrationtools/terminal.go
package integrationtools

func NewTerminalTool(workDir string) *TerminalTool {
    return &TerminalTool{workDir: workDir, sessions: map[string]*ptySession{}}
}

func (t *TerminalTool) Name() string { return "terminal" }

func (t *TerminalTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
    var p TerminalParams
    if err := json.Unmarshal(params, &p); err != nil { /* ... */ }

    switch p.Action {
    case "start":
        return t.start(ctx, p)      // pty.Start(exec.Command("bash","-lc",p.Command)) + emulator
    case "send_keys":
        return t.sendKeys(ctx, p)   // encode keys -> write ptmx -> settle -> snapshot
    case "read_screen":
        return t.readScreen(ctx, p)
    case "wait_for":
        return t.waitFor(ctx, p)
    case "resize":
        return t.resize(ctx, p)     // pty.Setsize + term.Resize
    case "interrupt":
        return t.sendRaw(ctx, p.Session, []byte{0x03})
    case "stop":
        return t.stop(ctx, p)
    case "list":
        return t.list(ctx)
    default:
        return &tools.Result{Success: false, Error: "unknown action"}, nil
    }
}
```

`start` (core of it):

```go
cmd := exec.Command("bash", "-lc", p.Command)
cmd.Dir = t.workDir
ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
// term := vt.NewTerminal(cols, rows)   // emulator
go func() {                              // drain master -> emulator
    buf := make([]byte, 4096)
    for {
        n, err := ptmx.Read(buf)
        if n > 0 { s.term.Write(buf[:n]); s.lastByteAt.Store(time.Now().UnixNano()) }
        if err != nil { break }
    }
    s.exitErr = cmd.Wait(); close(s.exited)
}()
return t.settleAndSnapshot(s, quietMs, timeoutMs)
```

Snapshot flattens `term`'s grid rows to strings and prepends the header.

## Testing strategy

- **Unit: key encoder** — table test notation → byte sequences.
- **Unit: emulator projection** — feed known escape sequences, assert the grid
  text (borrow fixtures from `vt10x`/`x/vt` tests).
- **Integration with deterministic TUIs** — drive real small programs that exist
  everywhere: `cat` (echo/line-editing), `less` (paging: `Space`, `q`), `vi`
  (`i…<Esc>:wq`), `bash -i` (prompt detection with `wait_for`). Assert screen
  content and files written. These are hermetic and fast, unlike `htop`.
- **Lifecycle** — start → stop kills the child; exited process reported, not
  written to; session cap enforced; resize triggers repaint.
- Gate flaky/interactive integration tests behind a build tag or env check, as
  the camera/browser tools already do.

## Open questions

1. **Emulator library** — resolved for the dependency axis: `hinshun/vt10x`
   (zero new transitive deps) over `charmbracelet/x/vt` (pulls `ultraviolet` +
   ~5 modules). Open only on fidelity: sanity-check `vt10x` against `htop` +
   `vim` and keep `x/vt` as the upgrade path if a real app renders wrong or we
   need semantic color/style.
2. **One tool, many sessions vs one tool per session** — the `session` key
   supports parallel TUIs; confirm the agent loop won't accidentally serialize or
   deadlock on the shared mutex when juggling two.
3. **Color/style exposure** — is plain text enough, or do we need an optional
   ANSI-preserving / style-map mode for apps where color is semantic?
4. **Screenshot bridge** — should `read_screen` optionally render the grid to a
   PNG (reusing `take_screenshot` infra) for the multimodal path, mirroring
   `browser_chrome`'s `screenshot`?
5. **Windows** — `creack/pty` is Unix-oriented; ConPTY support is separate. Scope
   to macOS/Linux first (consistent with the macOS-specific browser/camera tools).

## Summary

The terminal-emulation tool is the TUI sibling of `browser_chrome`: a persistent,
mutex-guarded, action-based tool that spawns a program in a **PTY** so it behaves
interactively, runs the byte stream through a **VT emulator** to keep a real
screen grid, and exposes `start / send_keys / read_screen / wait_for` so the
agent can operate `vim`, `htop`, `lazygit`, `ssh`, or any curses app as a human
would. The two genuinely new pieces are a PTY dependency (`creack/pty`) and a VT
emulator (`x/vt` or `vt10x`); everything else — the tool interface, lazy
lifecycle with liveness checks, progress streaming, registration — is already
established in the codebase. The subtle engineering is **synchronization**
(knowing when the screen has settled) and **key encoding** (human notation →
control bytes), both addressed above.
