package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gratheon/aagent/internal/speechcache"
)

func TestMossRealtimeTTSToolExecuteViaHTTPService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/synthesize" {
			t.Fatalf("expected /synthesize, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF____WAVEfmt "))
	}))
	defer server.Close()

	tool := NewMossRealtimeTTSTool(t.TempDir(), speechcache.New(0))
	params := map[string]interface{}{
		"text":         "hello from moss docker service",
		"output_mode":  "stream",
		"endpoint_url": server.URL,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Clip ID:") {
		t.Fatalf("expected clip id in output, got: %s", res.Output)
	}
	audioMeta, ok := res.Metadata["audio_clip"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing audio_clip metadata")
	}
	if strings.TrimSpace(asStringMoss(audioMeta["clip_id"])) == "" {
		t.Fatalf("missing clip_id in audio_clip metadata")
	}
}

func asStringMoss(v interface{}) string {
	s, _ := v.(string)
	return s
}
