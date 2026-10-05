package contextcompress

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestAdmissionCap(t *testing.T) {
	for _, name := range []string{"browser_chrome", "fetch_url", "chrome_extension", "context_retrieve", "read"} {
		t.Run(name, func(t *testing.T) {
			sess := session.New("agent")
			c := NewCompressor(Config{}) // Admission is independent of optional compression.
			original := "HEAD\n" + strings.Repeat("noise 漢字\n", 5000) + "hidden target\nTAIL"
			tr := llm.ToolResult{ToolCallID: "call", Name: name, Content: original, IsError: true, DurationMs: 17, Metadata: map[string]interface{}{"source": "test"}}
			got := c.AdmitToolResult(sess, tr, 256)
			if utf8.RuneCountInString(got.Content) > 256*4 {
				t.Fatalf("cap exceeded: %d runes", utf8.RuneCountInString(got.Content))
			}
			for _, want := range []string{"HEAD", "TAIL", "brute-compressed", "context_retrieve", "admission"} {
				if !strings.Contains(got.Content, want) {
					t.Errorf("missing %q", want)
				}
			}
			if !utf8.ValidString(got.Content) {
				t.Fatal("broken UTF-8")
			}
			got.Content = original
			if !reflect.DeepEqual(got, tr) {
				t.Fatal("tool result fields changed")
			}
			stored := readSessionEntries(sess.Metadata)
			if len(stored) != 1 {
				t.Fatalf("stored %d entries", len(stored))
			}
			for hash := range stored {
				full, ok := c.Retrieve(sess.ID, hash, "")
				if !ok || full != original {
					t.Fatal("original not retrievable")
				}
				matched, ok := c.Retrieve(sess.ID, hash, "hidden target")
				if !ok || !strings.Contains(matched, "hidden target") || strings.Contains(matched, "noise") {
					t.Fatal("query mismatch")
				}
				if _, ok := c.Retrieve("other-session", hash, ""); ok {
					t.Fatal("cross-session retrieval")
				}
			}
		})
	}
}

func TestAdmissionUnderCapUntouched(t *testing.T) {
	for _, original := range []string{"small", strings.Repeat("界", 1024), strings.Repeat(" ", 1024)} {
		sess := session.New("agent")
		tr := llm.ToolResult{Name: "browser_chrome", Content: original, Metadata: map[string]interface{}{"image_inline": map[string]interface{}{"media_type": "image/png", "data_base64": strings.Repeat("YQ==", 10000)}}}
		got := NewCompressor(Config{}).AdmitToolResult(sess, tr, 256)
		if !reflect.DeepEqual(got, tr) {
			t.Fatal("under-cap result changed (image metadata must not count)")
		}
		if len(readSessionEntries(sess.Metadata)) != 0 {
			t.Fatal("under-cap original stored")
		}
	}
}

func TestAdmissionPersistedRetrieval(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("agent")
	if err != nil {
		t.Fatal(err)
	}
	original := strings.Repeat("line\n", 10000)
	c := NewCompressorWithSessionStore(Config{}, sm)
	got := c.AdmitToolResult(sess, llm.ToolResult{Name: "fetch_url", ToolCallID: "call", Content: original}, 256)
	sess.AddToolResult([]session.ToolResult{{Name: "fetch_url", ToolCallID: "call", Content: got.Content}})
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	fresh := NewCompressorWithSessionStore(Config{}, sm)
	for hash := range readSessionEntries(sess.Metadata) {
		params, _ := json.Marshal(RetrieveParams{Hash: hash})
		result, err := NewRetrieveTool(fresh).Execute(context.WithValue(context.Background(), "session_id", sess.ID), params)
		if err != nil || !result.Success || result.Output != original {
			t.Fatalf("retrieval failed: %v", err)
		}
	}
}

func TestAdmissionSeparatesScreenshotDataURL(t *testing.T) {
	payload := strings.Repeat("YQ==", 10000)
	content := `{"ok":true,"result":{"data_url":"data:image/png;base64,` + payload + `","media_type":"image/png"}}`
	sess := session.New("agent")
	got := NewCompressor(Config{}).AdmitToolResult(sess, llm.ToolResult{Name: "chrome_extension", Content: content}, 256)
	if strings.Contains(got.Content, payload) || strings.Contains(got.Content, "brute-compressed") {
		t.Fatal("screenshot counted or left in text")
	}
	inline, ok := got.Metadata["image_inline"].(map[string]interface{})
	if !ok || inline["data_base64"] != payload || inline["media_type"] != "image/png" {
		t.Fatal("screenshot lost")
	}
	if len(readSessionEntries(sess.Metadata)) != 0 {
		t.Fatal("image-only output should not trigger cap")
	}
}

func TestAdmissionMultipleImagesAndExactOriginal(t *testing.T) {
	payload := strings.Repeat("YQ==", 10000)
	original := `{"images":[{"data_url":"data:image/png;base64,` + payload + `"},{"media_type":"image/png","data_base64":"` + payload + `"}],"text":"` + strings.Repeat("x", 10000) + `"}`
	sess := session.New("agent")
	got := NewCompressor(Config{}).AdmitToolResult(sess, llm.ToolResult{Name: "chrome_extension", Content: original, Metadata: map[string]interface{}{"image_inline": map[string]interface{}{"media_type": "image/jpeg", "data_base64": "old"}}}, 256)
	if strings.Contains(got.Content, payload) {
		t.Fatal("base64 retained in text")
	}
	images, ok := got.Metadata["admission_images"].([]interface{})
	if !ok || len(images) != 3 {
		t.Fatal("images lost")
	}
	for _, entry := range readSessionEntries(sess.Metadata) {
		if entry.Original != original {
			t.Fatal("stored original is not exact")
		}
	}
}

func TestAdmissionDoesNotRecompressExcerpt(t *testing.T) {
	sess := session.New("agent")
	c := NewCompressor(Config{Enabled: true})
	tr := c.AdmitToolResult(sess, llm.ToolResult{Name: "fetch_url", ToolCallID: "call", Content: strings.Repeat("noise\n", 50000)}, 8000)
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{tr}}}}
	got, result := c.CompressRequest(context.Background(), sess.ID, req)
	if result.Applied || got.Messages[0].ToolResults[0].Content != tr.Content {
		t.Fatal("admission excerpt recompressed")
	}
}

func TestAdmissionWrapperScreenshot(t *testing.T) {
	child, _ := json.Marshal(map[string]string{"data_url": "data:image/png;base64," + strings.Repeat("YQ==", 10000)})
	wrapper, _ := json.Marshal([]interface{}{map[string]interface{}{"tool": "chrome_extension", "output": string(child)}})
	sess := session.New("agent")
	got := NewCompressor(Config{}).AdmitToolResult(sess, llm.ToolResult{Name: "parallel", Content: string(wrapper)}, 256)
	if strings.Contains(got.Content, "YQ==") || strings.Contains(got.Content, "brute-compressed") {
		t.Fatal("wrapper image counted as text")
	}
	if len(got.Metadata["admission_images"].([]interface{})) != 1 {
		t.Fatal("wrapper image lost")
	}
}
