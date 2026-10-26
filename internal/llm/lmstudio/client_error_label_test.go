package lmstudio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestErrorLabelNamesProxiedProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"denied"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	client := NewClient("k", "m", srv.URL)
	client.ErrorLabel = "cursor (via parent proxy)"
	_, err := client.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.HasPrefix(err.Error(), "cursor (via parent proxy) error (403)") {
		t.Fatalf("err = %v, want proxied provider label", err)
	}
}
