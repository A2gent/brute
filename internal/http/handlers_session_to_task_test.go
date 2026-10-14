package http

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestBuildTaskFromSession(t *testing.T) {
	parent := "parent-1"
	sess := &session.Session{ID: "s1", Title: "Fix login", ParentID: &parent, Messages: []session.Message{
		{Role: "user", Content: "please fix", Images: []session.ImageAttachment{{Name: "a.png", MediaType: "image/png", DataBase64: "AAAA"}, {URL: "http://x"}}},
		{Role: "assistant", Content: "ignored"},
		{Role: "user", Content: "more"},
	}}
	got := buildTaskFromSession(sess)
	if got.Title != "Fix login" || got.Status != "todo" {
		t.Fatalf("unexpected title/status: %+v", got)
	}
	if got.Body != "Parent session: parent-1\n\nplease fix\n\nmore" {
		t.Fatalf("unexpected body: %q", got.Body)
	}
	if len(got.Images) != 1 || got.Images[0].Name != "a.png" {
		t.Fatalf("expected one embedded image, got %+v", got.Images)
	}
}
