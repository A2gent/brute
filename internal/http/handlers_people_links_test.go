package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestListProjectPeopleResolvesMarkdownRelationships(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	root := t.TempDir()
	peopleRoot := filepath.Join(root, "People")
	if err := os.MkdirAll(filepath.Join(peopleRoot, "Friends"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(peopleRoot, "Family"), 0o755); err != nil {
		t.Fatal(err)
	}

	cards := map[string]string{
		"Friends/Alex.md": `---
type: person
name: Alex
importance: 8
---

[Bob](../Family/Bob%20Example.md)
[[Cara Example|Cara]]
![[Bob Example]]
[External](https://example.com/person.md)
`,
		"Family/Bob Example.md": `---
type: person
name: Bob
importance: 7
---

[Alex](../Friends/Alex.md#notes)
`,
		"Family/Cara Example.md": `---
type: person
name: Cara Example
importance: 6
aliases:
  - C. Example
---
`,
	}
	for path, content := range cards {
		if err := os.WriteFile(filepath.Join(peopleRoot, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	project := createTestProject(t, store, "people-graph-project", "People graph", root)
	req := httptest.NewRequest(http.MethodGet, "/projects/people-graph-project/people", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("projectID", project.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	server.handleListProjectPeople(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response projectPeopleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	byName := make(map[string]projectPerson, len(response.People))
	for _, person := range response.People {
		byName[person.Name] = person
	}
	alexLinks := byName["Alex"].Links
	if len(alexLinks) != 2 || alexLinks[0] != "People/Family/Bob Example.md" || alexLinks[1] != "People/Family/Cara Example.md" {
		t.Fatalf("Alex links = %#v", alexLinks)
	}
	bobLinks := byName["Bob"].Links
	if len(bobLinks) != 1 || bobLinks[0] != "People/Friends/Alex.md" {
		t.Fatalf("Bob links = %#v", bobLinks)
	}
	if links := byName["Cara Example"].Links; links == nil || len(links) != 0 {
		t.Fatalf("Cara links = %#v, want an empty JSON array", links)
	}
}
