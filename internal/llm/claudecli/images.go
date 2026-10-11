package claudecli

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

// WHY: `claude -p <prompt>` only accepts text, so inline images were dropped and
// the model reported that no picture was received. Claude CLI accepts native
// image blocks through `--input-format stream-json` on stdin instead.

type streamImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type streamContentBlock struct {
	Type   string             `json:"type"`
	Text   string             `json:"text,omitempty"`
	Source *streamImageSource `json:"source,omitempty"`
}

type streamUserEnvelope struct {
	Type    string `json:"type"`
	Message struct {
		Role    string               `json:"role"`
		Content []streamContentBlock `json:"content"`
	} `json:"message"`
}

// hasInlineImages reports whether any message carries base64 image data.
func hasInlineImages(request *llm.ChatRequest) bool {
	return len(collectInlineImages(request)) > 0
}

func collectInlineImages(request *llm.ChatRequest) []llm.Image {
	if request == nil {
		return nil
	}
	var out []llm.Image
	for _, msg := range request.Messages {
		for _, img := range msg.Images {
			if strings.TrimSpace(img.MediaType) != "" && strings.TrimSpace(img.DataBase64) != "" {
				out = append(out, img)
			}
		}
	}
	return out
}

// buildStreamInput encodes the prompt plus all inline images as one stream-json user message.
func buildStreamInput(request *llm.ChatRequest, prompt string) ([]byte, error) {
	var env streamUserEnvelope
	env.Type = "user"
	env.Message.Role = "user"
	env.Message.Content = append(env.Message.Content, streamContentBlock{Type: "text", Text: prompt})
	for _, img := range collectInlineImages(request) {
		env.Message.Content = append(env.Message.Content, streamContentBlock{
			Type: "image",
			Source: &streamImageSource{
				Type:      "base64",
				MediaType: strings.TrimSpace(img.MediaType),
				Data:      strings.TrimSpace(img.DataBase64),
			},
		})
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// withStdinPrompt swaps the leading `-p <prompt>` pair for stdin stream-json input.
func withStdinPrompt(args []string) []string {
	out := []string{"-p", "--input-format", "stream-json"}
	return append(out, args[2:]...)
}
