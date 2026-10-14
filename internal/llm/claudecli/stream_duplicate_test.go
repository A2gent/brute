package claudecli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

// runFakeClaude runs ChatStream against a fake claude executable that prints the given stream-json lines.
func runFakeClaude(t *testing.T, lines ...string) (*llm.ChatResponse, []string) {
	t.Helper()
	tmp := t.TempDir()
	fakeClaude := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\n"
	for _, line := range lines {
		script += "printf '%s\\n' '" + line + "'\n"
	}
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake claude: %v", err)
	}
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")
	client := NewClientWithOptions("claude-sonnet-4-6", Options{Executable: fakeClaude, WorkDir: tmp, NoSessionPersistence: true})
	var deltas []string
	resp, err := client.ChatStream(t.Context(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}}, func(event llm.StreamEvent) error {
		if event.Type == llm.StreamEventContentDelta {
			deltas = append(deltas, event.ContentDelta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	return resp, deltas
}

const sessionID = "018b187a-7b35-444c-8d6f-92b90e8d7b64"

func TestFinalResultIsNotAppendedWhenAssistantMessageHasIt(t *testing.T) {
	resp, deltas := runFakeClaude(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello! How can I help?"}]},"session_id":"`+sessionID+`"}`,
		`{"type":"result","subtype":"success","result":"Hello! How can I help?","session_id":"`+sessionID+`","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":6}}`,
	)
	if resp.Content != "Hello! How can I help?" {
		t.Fatalf("reply duplicated or altered: %q", resp.Content)
	}
	if got := strings.Join(deltas, ""); got != "Hello! How can I help?" {
		t.Fatalf("streamed text duplicated: %q", got)
	}
}

func TestFinalResultStillAddedWhenItDiffers(t *testing.T) {
	resp, _ := runFakeClaude(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Let me check."}]},"session_id":"`+sessionID+`"}`,
		`{"type":"result","subtype":"success","result":"All done.","session_id":"`+sessionID+`","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":6}}`,
	)
	if resp.Content != "Let me check.\n\nAll done." {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
}

func TestFinalResultNotDuplicatedAfterEarlierAssistantText(t *testing.T) {
	// Text before a tool call plus the final answer: the result equals only the last part.
	resp, _ := runFakeClaude(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Checking."}]},"session_id":"`+sessionID+`"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The answer is 42."}]},"session_id":"`+sessionID+`"}`,
		`{"type":"result","subtype":"success","result":"The answer is 42.","session_id":"`+sessionID+`","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":6}}`,
	)
	if strings.Count(resp.Content, "The answer is 42.") != 1 {
		t.Fatalf("final answer repeated: %q", resp.Content)
	}
}
