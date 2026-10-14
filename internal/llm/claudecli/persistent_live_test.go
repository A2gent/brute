package claudecli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

// Opt-in: this spends real Claude quota. Keep ordinary unit tests offline.
func TestLivePersistentTTFT(t *testing.T) {
	if os.Getenv("AAGENT_CLAUDE_TTFT_LIVE") != "1" {
		t.Skip("set AAGENT_CLAUDE_TTFT_LIVE=1 to measure real CLI turns")
	}
	t.Setenv("AAGENT_CLAUDE_CLI_PERSISTENT", "false")
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent_%t", persistent), func(t *testing.T) {
			opts := Options{WorkDir: t.TempDir(), Persistent: persistent}
			sessionID := fmt.Sprintf("ttft-%d", time.Now().UnixNano())
			req := &llm.ChatRequest{SessionID: sessionID, SystemPrompt: "Reply with exactly OK. Do not use tools."}
			for turn := 1; turn <= 5; turn++ {
				req.Messages = []llm.Message{{Role: "user", Content: fmt.Sprintf("Turn %d. Reply OK.", turn)}}
				ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
				client := NewClientWithOptions("sonnet", opts)
				// NewClient normalizes workdir/executable; cleanup must use that pool key.
				defer closePersistentSession(client.options, sessionID)
				resp, err := client.ChatStream(ctx, req, nil)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				req.ProviderSessionCursor = resp.ProviderSessionCursor
				data, _ := json.Marshal(resp.TimingMetadata)
				t.Logf("turn=%d %s", turn, data)
				if persistent && turn > 1 && resp.TimingMetadata["llm_process_reused"] != true {
					t.Fatal("process was not reused")
				}
			}
		})
	}
}
