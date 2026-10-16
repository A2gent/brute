package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/openaicodex"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type prefixWire struct {
	Tools        json.RawMessage   `json:"tools"`
	Instructions json.RawMessage   `json:"instructions"`
	Input        []json.RawMessage `json:"input"`
}

// Compare actual serialized Responses payloads, including encrypted reasoning
// and normalized screenshots, rather than just ChatRequest source structs.
func TestBuildRequestPreservesResponsesCachePrefix(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(map[bool]string{false: "admission_only", true: "compression"}[compress], func(t *testing.T) {
			var captured prefixWire
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				captured = prefixWire{}
				if err := json.Unmarshal(body, &captured); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"resp_test","output":[]}`)
			}))
			defer server.Close()
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sess, err := sm.Create("prefix-test")
			if err != nil {
				t.Fatal(err)
			}
			tm := tools.NewManager(t.TempDir())
			client := openaicodex.NewClientWithOptions("test", "test", server.URL, openaicodex.Options{PublicAPI: true})
			ag := New(Config{SystemPrompt: "Stable instructions", CompressToolResults: compress, ToolResultMaxTokens: 512}, client, tm, sm)
			var previous *prefixWire
			check := func() {
				t.Helper()
				if _, err := client.Chat(t.Context(), ag.buildRequest(context.Background(), sess)); err != nil {
					t.Fatal(err)
				}
				if previous != nil {
					if !bytes.Equal(previous.Tools, captured.Tools) {
						t.Fatal("tools changed")
					}
					if !bytes.Equal(previous.Instructions, captured.Instructions) {
						t.Fatal("instructions changed after a tool result")
					}
					if len(captured.Input) < len(previous.Input) {
						t.Fatal("input prefix shrank")
					}
					for i, raw := range previous.Input {
						if !bytes.Equal(raw, captured.Input[i]) {
							t.Fatalf("input item %d changed", i)
						}
					}
				}
				copy := captured
				previous = &copy
			}
			sess.AddUserMessage("inspect, test, then capture")
			check()
			check()
			for i, content := range []string{"short result", strings.Repeat("noise\n", 3000), "screenshot"} {
				id := []string{"short", "large", "image"}[i]
				reasoning := json.RawMessage(`{"type":"reasoning","id":"rs_` + id + `","summary":[],"encrypted_content":"opaque+/=="}`)
				metadata := map[string]interface{}{}
				preserveReasoningItems(metadata, &llm.ChatResponse{ReasoningItems: []json.RawMessage{reasoning}}, sess)
				sess.AddAssistantMessageWithImagesAndMetadata("", nil, []session.ToolCall{{ID: id, Name: "bash", Input: []byte(`{"command":"fixture"}`)}}, metadata)
				result := session.ToolResult{ToolCallID: id, Name: "bash", Content: content}
				if i == 2 {
					result.Metadata = map[string]interface{}{"admission_images": []interface{}{map[string]interface{}{"media_type": "image/png", "data_base64": "c3ludGhldGlj"}}, "image_inline": "legacy"}
				}
				sess.AddToolResult([]session.ToolResult{result})
				check()
				check()
				if err := sm.Save(sess); err != nil {
					t.Fatal(err)
				}
				sess, err = session.NewManager(store).Get(sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				check()
			}
			found := 0
			for _, raw := range captured.Input {
				var item struct{ Type string }
				_ = json.Unmarshal(raw, &item)
				if item.Type == "reasoning" {
					found++
					if !bytes.Contains(raw, []byte(`"encrypted_content":"opaque+/=="`)) {
						t.Fatalf("encrypted reasoning changed: %s", raw)
					}
				}
			}
			if found != 3 {
				t.Fatalf("reasoning items = %d", found)
			}
		})
	}
}
