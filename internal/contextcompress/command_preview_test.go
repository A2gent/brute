package contextcompress

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

func TestCommandPreviewThroughAdmissionAndWrappers(t *testing.T) {
	raw := strings.Repeat("ordinary line\n", 2500) + "ERROR middle diagnostic\n" + strings.Repeat("ordinary tail\n", 2500) + "Exit code: 7"
	metadata := map[string]interface{}{"exit_code": 7}
	for _, name := range []string{"bash", "pipeline", "parallel"} {
		t.Run(name, func(t *testing.T) {
			content := raw
			meta := metadata
			if name == "pipeline" {
				meta = map[string]interface{}{"command_output": true}
			}
			if name == "parallel" {
				data, _ := json.MarshalIndent([]map[string]interface{}{{"tool": "bash", "output": raw, "metadata": metadata}}, "", "  ")
				content = string(data)
			}
			c := NewCompressor(Config{Enabled: true})
			sess := session.New("agent")
			tr := c.AdmitToolResult(sess, llm.ToolResult{Name: name, Content: content, Metadata: meta}, 8000)
			req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{tr}}}}
			out, result := c.CompressRequest(context.Background(), sess.ID, req)
			if !result.Applied {
				t.Fatal("expected command preview")
			}
			got := out.Messages[0].ToolResults[0].Content
			for _, want := range []string{"ERROR middle diagnostic", "Exit code: 7", "[repeated 2500 times]"} {
				if !strings.Contains(got, want) {
					t.Fatalf("lost %q", want)
				}
			}
			recovered, ok := c.Retrieve(sess.ID, result.Items[0].Hash, "")
			if !ok || recovered != content {
				t.Fatal("original lost")
			}
			if _, ok := c.Retrieve("another-session", result.Items[0].Hash, ""); ok {
				t.Fatal("cross session leak")
			}
		})
	}
}

func TestGrepFullOutputBypassesRequestCompression(t *testing.T) {
	original := strings.Repeat("a.go:1:needle\n", 1500)
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{Name: "grep", Content: original, Metadata: map[string]interface{}{"full_output": true}}}}}}
	out, result := NewCompressor(Config{Enabled: true}).CompressRequest(context.Background(), "s", req)
	if result.Applied || out.Messages[0].ToolResults[0].Content != original {
		t.Fatal("full_output was compressed")
	}
}

func TestCommandAdmissionKeepsErrorsWithoutRequestCompression(t *testing.T) {
	raw := strings.Repeat("ordinary ready\n", 2500) + "ERROR middle diagnostic\n" + strings.Repeat("ordinary ready\n", 2500) + "Exit code: 1"
	c := NewCompressor(Config{Enabled: false})
	sess := session.New("agent")
	tr := c.AdmitToolResult(sess, llm.ToolResult{Name: "bash", Content: raw}, 128)
	if !strings.Contains(tr.Content, "ERROR middle diagnostic") || !strings.Contains(tr.Content, "Exit code: 1") {
		t.Fatal("admission lost diagnostics")
	}
	if !strings.Contains(tr.Content, "repeated 5000") && !strings.Contains(tr.Content, "repeated 2500") {
		t.Fatal("admission did not collapse")
	}
}
