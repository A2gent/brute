package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/cursorcli"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestCursorProviderSessionResumesAcrossTwoTurns(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	tmp := t.TempDir()
	// The fake stores chats where Cursor does, so the client can verify them.
	t.Setenv("HOME", t.TempDir())
	argsFile := filepath.Join(tmp, "args.txt")
	script := `#!/bin/sh
call=1
if [ -f "$CALL_FILE" ]; then call=$(($(cat "$CALL_FILE") + 1)); fi
printf '%s' "$call" > "$CALL_FILE"
printf '%s\0' "$@" >> "$ARGS_FILE"
printf '\036' >> "$ARGS_FILE"
id="99999999-0000-0000-0000-00000000000$call"
mkdir -p "$HOME/.cursor/chats/ws/$id"
printf '{"type":"result","subtype":"success","result":"answer %s","session_id":"%s","usage":{"inputTokens":1,"outputTokens":1}}\n' "$call" "$id"
`
	fake := filepath.Join(tmp, "agent")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake agent: %v", err)
	}
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("CALL_FILE", filepath.Join(tmp, "calls.txt"))
	t.Setenv("AAGENT_CURSOR_CLI_PATH", "")

	settings := config.ResolveProviderSessionSettings("cursor", config.Provider{APIKey: "k"})
	if !settings.UseProviderSession || !strings.HasPrefix(settings.ProviderSessionIdentity, "cursor:") {
		t.Fatalf("cursor provider sessions must be enabled with a cursor identity, got %+v", settings)
	}
	client := cursorcli.NewClientWithOptions("composer-2.5", cursorcli.Options{Executable: fake, WorkDir: tmp, APIKey: "k"})
	ag := New(Config{
		Model:                   "composer-2.5",
		MaxSteps:                10,
		UseProviderSession:      true,
		ProviderSessionIdentity: settings.ProviderSessionIdentity,
	}, client, tools.NewManager(t.TempDir()), sm)

	sess.AddUserMessage("first prompt")
	if _, _, err := ag.RunWithEvents(context.Background(), sess, "first prompt", nil); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	sess.AddUserMessage("second prompt")
	if _, _, err := ag.RunWithEvents(context.Background(), sess, "second prompt", nil); err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	calls := readAgentFakeClaudeCalls(t, argsFile)
	if len(calls) != 2 {
		t.Fatalf("fake Cursor calls = %d, want 2", len(calls))
	}
	assertClaudeArgsContainPair(t, calls[1], "--resume", "99999999-0000-0000-0000-000000000001")
	assertClaudeArgsContainPair(t, calls[1], "-p", "second prompt")
	if strings.Contains(strings.Join(calls[1], "\n"), "first prompt") {
		t.Fatalf("turn 2 args unexpectedly contain prior transcript: %#v", calls[1])
	}
}
