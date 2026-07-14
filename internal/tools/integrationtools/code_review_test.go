package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestCodeReviewToolListsAndRepliesToBitbucketComments(t *testing.T) {
	var replyBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pullrequests/42/comments"):
			_, _ = w.Write([]byte(`{"values":[{"id":7,"content":{"raw":"Please add a test"},"user":{"display_name":"Reviewer"},"created_on":"2026-07-14T07:00:00Z"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pullrequests/42/comments"):
			var body struct {
				Content struct {
					Raw string `json:"raw"`
				} `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			replyBody = body.Content.Raw
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":8,"parent":{"id":7},"content":{"raw":"Added"},"user":{"display_name":"Author"},"created_on":"2026-07-14T08:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if err := store.SaveIntegration(&storage.Integration{ID: "bb", Provider: "bitbucket", Name: "BB", Mode: "duplex", Enabled: true, Config: map[string]string{"email": "dev@example.com", "api_token": "secret", "api_base_url": server.URL}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	tool := NewCodeReviewTool(store)
	tool.client = server.Client()
	listed, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"list_comments","provider":"bitbucket","owner":"acme","repository":"widgets","pull_request_id":"42"}`))
	if err != nil || !listed.Success || !strings.Contains(listed.Output, "Please add a test") {
		t.Fatalf("list comments failed: result=%#v err=%v", listed, err)
	}
	replied, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"reply","provider":"bitbucket","owner":"acme","repository":"widgets","pull_request_id":"42","comment_id":"7","body":"Added"}`))
	if err != nil || !replied.Success || replyBody != "Added" {
		t.Fatalf("reply failed: result=%#v body=%q err=%v", replied, replyBody, err)
	}
}
