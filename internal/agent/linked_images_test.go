package agent

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

func TestLinkedContinuationImagesReachLLMRequest(t *testing.T) {
	sess := session.New("build")
	payload := strings.Repeat("YQ==", 10000)
	sess.AddUserMessageWithImagesAndMetadata("Continue from compact parent context", []session.ImageAttachment{
		{Name: "reference.png", MediaType: "image/png", DataBase64: payload},
		{MediaType: "image/jpeg", URL: "https://example.com/screenshot.jpg"},
	}, map[string]interface{}{"linked_context": true, "context_mode": "compact"})
	ag := New(Config{}, nil, tools.NewManager(t.TempDir()), nil)
	request := ag.buildRequest(sess)
	if len(request.Messages) != 1 || len(request.Messages[0].Images) != 2 {
		t.Fatalf("linked images missing from LLM request: %#v", request.Messages)
	}
	if request.Messages[0].Images[0].DataBase64 != payload || request.Messages[0].Images[1].URL != "https://example.com/screenshot.jpg" {
		t.Fatal("linked image data changed during request building")
	}
}
