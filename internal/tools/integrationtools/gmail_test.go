package integrationtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func b64(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

func newGmailFixture(t *testing.T, level string, handler http.HandlerFunc) (*storage.SQLiteStore, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldBase := gmailAPIBase
	gmailAPIBase = srv.URL
	t.Cleanup(func() { gmailAPIBase = oldBase })

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIntegration(&storage.Integration{
		ID: "g1", Provider: GmailProvider, Name: "Gmail", Mode: "notify_only", Enabled: true,
		Config: map[string]string{
			"client_id": "id", "client_secret": "secret", "refresh_token": "rt",
			"access_token": "tok", "token_expiry": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"access_level": level,
		},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return store, srv
}

func TestGmailQuerySearchAndRead(t *testing.T) {
	store, _ := newGmailFixture(t, "read", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer token")
		}
		switch r.URL.Path {
		case "/messages":
			if got := r.URL.Query().Get("q"); got != "in:inbox is:unread" {
				t.Errorf("query = %q", got)
			}
			_, _ = w.Write([]byte(`{"messages":[{"id":"m1"}]}`))
		case "/messages/m1":
			_, _ = w.Write([]byte(`{"id":"m1","threadId":"t1","snippet":"hi","labelIds":["INBOX","UNREAD"],"payload":{"mimeType":"multipart/alternative","headers":[{"name":"From","value":"a@b.c"},{"name":"Subject","value":"Hello"}],"parts":[{"mimeType":"text/html","body":{"data":"` + b64("<p>html</p>") + `"}},{"mimeType":"text/plain","body":{"data":"` + b64("plain body") + `"}}]}}`))
		default:
			http.NotFound(w, r)
		}
	})
	tool := NewGmailQueryTool(store)

	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"operation":"search","query":"in:inbox is:unread"}`))
	if !res.Success || !strings.Contains(res.Output, `"subject": "Hello"`) || !strings.Contains(res.Output, `"unread": true`) {
		t.Fatalf("search: %+v", res)
	}
	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"operation":"read_message","message_id":"m1"}`))
	if !res.Success || !strings.Contains(res.Output, "plain body") || strings.Contains(res.Output, "html</p>") {
		t.Fatalf("read_message: %+v", res)
	}
}

func TestGmailDraftRefusedWhenReadOnly(t *testing.T) {
	store, _ := newGmailFixture(t, "read", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no API call expected, got %s", r.URL.Path)
	})
	res, _ := NewGmailDraftTool(store).Execute(context.Background(), json.RawMessage(`{"to":"x@y.z","body":"hi"}`))
	if res.Success || !strings.Contains(res.Error, "read-only") {
		t.Fatalf("expected read-only refusal, got %+v", res)
	}
}

func TestGmailDraftReplyKeepsThread(t *testing.T) {
	var posted map[string]map[string]interface{}
	store, _ := newGmailFixture(t, "draft", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/messages/m1":
			_, _ = w.Write([]byte(`{"id":"m1","threadId":"t1","payload":{"headers":[{"name":"From","value":"Bob <bob@example.com>"},{"name":"Subject","value":"Lunch?"},{"name":"Message-ID","value":"<abc@x>"}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/drafts":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &posted)
			_, _ = w.Write([]byte(`{"id":"d1","message":{"id":"m2","threadId":"t1"}}`))
		default:
			http.NotFound(w, r)
		}
	})
	res, _ := NewGmailDraftTool(store).Execute(context.Background(), json.RawMessage(`{"reply_to_message_id":"m1","body":"Sure!"}`))
	if !res.Success {
		t.Fatalf("draft: %+v", res)
	}
	msg := posted["message"]
	if msg["threadId"] != "t1" {
		t.Fatalf("threadId = %v", msg["threadId"])
	}
	raw, _ := base64.URLEncoding.DecodeString(msg["raw"].(string))
	text := string(raw)
	for _, want := range []string{"To: Bob <bob@example.com>", "Subject: Re: Lunch?", "In-Reply-To: <abc@x>"} {
		if !strings.Contains(text, want) {
			t.Errorf("raw message missing %q:\n%s", want, text)
		}
	}
}

func TestGmailScopesByLevel(t *testing.T) {
	if s := strings.Join(GmailScopes(GmailAccessRead), " "); strings.Contains(s, "compose") {
		t.Fatalf("read level must not request compose: %s", s)
	}
	if s := strings.Join(GmailScopes(GmailAccessDraft), " "); !strings.Contains(s, "gmail.compose") || strings.Contains(s, "gmail.send") {
		t.Fatalf("draft level scopes wrong: %s", s)
	}
}

func TestSanitizeHeaderStripsNewlines(t *testing.T) {
	if got := sanitizeHeader("a\r\nBcc: evil"); strings.ContainsAny(got, "\r\n") {
		t.Fatalf("newline survived: %q", got)
	}
}
