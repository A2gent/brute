package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestLinkProjectPersonTelegram(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	root := t.TempDir()
	peopleRoot := filepath.Join(root, "People")
	if err := os.MkdirAll(peopleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cards := map[string]string{
		"Anna.md":  "---\ntype: person\nname: Anna\nimportance: 5\nstatus: active\ncustom_field: keep-me\n---\n\n# Anna\n\nNotes stay.\n",
		"Boris.md": "---\ntype: person\nname: Boris\nimportance: 5\nstatus: active\n---\n\n# Boris\n",
	}
	for name, content := range cards {
		if err := os.WriteFile(filepath.Join(peopleRoot, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	project := createTestProject(t, store, "tg-link-project", "Telegram links", root)

	call := func(body personTelegramLinkRequest) personTelegramLinkResponse {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/projects/x/people/telegram-link", bytes.NewReader(raw))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("projectID", project.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		rec := httptest.NewRecorder()
		server.handleLinkProjectPersonTelegram(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var resp personTelegramLinkResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	list := func() map[string]projectPerson {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/projects/x/people", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("projectID", project.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		rec := httptest.NewRecorder()
		server.handleListProjectPeople(rec, req)
		var resp projectPeopleResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		out := map[string]projectPerson{}
		for _, p := range resp.People {
			out[p.Name] = p
		}
		return out
	}

	call(personTelegramLinkRequest{Path: "People/Anna.md", ChatID: "-100123", Title: "Class 2B", Linked: true})
	people := list()
	if got := people["Anna"].TelegramChats; len(got) != 1 || got[0].ChatID != "-100123" || got[0].Title != "Class 2B" {
		t.Fatalf("Anna chats = %#v", got)
	}
	annaRaw, _ := os.ReadFile(filepath.Join(peopleRoot, "Anna.md"))
	if !strings.Contains(string(annaRaw), "custom_field: keep-me") || !strings.Contains(string(annaRaw), "Notes stay.") {
		t.Fatalf("card content lost:\n%s", annaRaw)
	}
	if got := people["Boris"].TelegramChats; got == nil || len(got) != 0 {
		t.Fatalf("Boris chats = %#v, want empty array", got)
	}

	// A chat belongs to one person: linking it to Boris moves it away from Anna.
	call(personTelegramLinkRequest{Path: "People/Boris.md", ChatID: "-100123", Title: "Class 2B", Linked: true})
	people = list()
	if len(people["Anna"].TelegramChats) != 0 || len(people["Boris"].TelegramChats) != 1 {
		t.Fatalf("after move: Anna=%#v Boris=%#v", people["Anna"].TelegramChats, people["Boris"].TelegramChats)
	}
	annaRaw, _ = os.ReadFile(filepath.Join(peopleRoot, "Anna.md"))
	if strings.Contains(string(annaRaw), "telegram_chats") || !strings.Contains(string(annaRaw), "custom_field: keep-me") {
		t.Fatalf("Anna card after move:\n%s", annaRaw)
	}

	if resp := call(personTelegramLinkRequest{ChatID: "-100123", Linked: false}); resp.Path != "" {
		t.Fatalf("unlink response path = %q", resp.Path)
	}
	if people = list(); len(people["Boris"].TelegramChats) != 0 {
		t.Fatalf("Boris chats after unlink = %#v", people["Boris"].TelegramChats)
	}
}

func TestSavePersonKeepsTelegramChats(t *testing.T) {
	existing := map[interface{}]interface{}{
		"type": "person", "name": "Anna", "importance": 5, "status": "active",
		personTelegramChatsKey: []map[string]string{{"chat_id": "42", "title": "Anna"}},
	}
	out, err := renderPersonMarkdown(existing, projectPersonRequest{Name: "Anna K", Importance: 6}, "# Anna\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "telegram_chats") || !strings.Contains(out, "42") {
		t.Fatalf("telegram_chats lost on save:\n%s", out)
	}
}
