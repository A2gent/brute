package claudecli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestBuildStreamInputIncludesImageBlocks(t *testing.T) {
	req := &llm.ChatRequest{Messages: []llm.Message{{
		Role:    "user",
		Content: "look",
		Images:  []llm.Image{{Name: "a.png", MediaType: "image/png", DataBase64: "QUJD"}, {Name: "no-data", URL: "http://x"}},
	}}}
	if !hasInlineImages(req) {
		t.Fatal("expected inline images")
	}
	data, err := buildStreamInput(req, buildPrompt(req))
	if err != nil {
		t.Fatal(err)
	}
	var env streamUserEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	blocks := env.Message.Content
	if len(blocks) != 2 || blocks[1].Type != "image" || blocks[1].Source.Data != "QUJD" || blocks[1].Source.MediaType != "image/png" {
		t.Fatalf("unexpected blocks: %+v", blocks)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("stream-json input must be newline terminated")
	}
}

func TestWithStdinPromptReplacesPromptArg(t *testing.T) {
	got := withStdinPrompt([]string{"-p", "--model", "m", "--", "hello"})
	want := []string{"-p", "--input-format", "stream-json", "--model", "m"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v want %v", got, want)
	}
}
