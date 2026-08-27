package http

import (
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

func TestListProjectPeopleReadsFrontmatterAndLegacyCards(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	root := t.TempDir()
	peopleRoot := filepath.Join(root, "08-Люди")
	if err := os.MkdirAll(filepath.Join(peopleRoot, "5 - коллеги", "img"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peopleRoot, "5 - коллеги", "img", "alex.jpg"), []byte("photo"), 0o644); err != nil {
		t.Fatal(err)
	}
	frontmatterCard := `---
type: person
name: Alex Example
photo: img/alex.jpg
groups:
  - Colleagues
importance: 8
relationship: colleague
company: Example Inc
role: Engineer
location: Tallinn
phones:
  - "+372 555 0101"
emails:
  - alex@example.com
socials:
  linkedin: https://linkedin.com/in/alex
interests:
  - AI
traits:
  - thoughtful
aliases:
  - Sasha
tags:
  - friend
status: active
custom_field: keep-me
---

# Notes

Met at work.
`
	if err := os.WriteFile(filepath.Join(peopleRoot, "5 - коллеги", "Alex Example.md"), []byte(frontmatterCard), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyCard := "# Мария Пример\n\n## Работа\n\nКомпания: Example\n\n![](maria.jpg)\n"
	if err := os.WriteFile(filepath.Join(peopleRoot, "5 - коллеги", "Мария Пример.md"), []byte(legacyCard), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peopleRoot, "5 - коллеги", "meeting_report_2026.md"), []byte("# Meeting report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := createTestProject(t, store, "people-project", "People", root)

	req := httptest.NewRequest(http.MethodGet, "/projects/people-project/people", nil)
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
	if response.Directory != "08-Люди" {
		t.Fatalf("directory = %q, want 08-Люди", response.Directory)
	}
	if len(response.People) != 2 {
		t.Fatalf("people count = %d, want 2: %+v", len(response.People), response.People)
	}
	alex := response.People[0]
	if alex.Name != "Alex Example" || alex.Importance != 8 || alex.Company != "Example Inc" {
		t.Fatalf("unexpected frontmatter person: %+v", alex)
	}
	if alex.PhotoPath != "08-Люди/5 - коллеги/img/alex.jpg" {
		t.Fatalf("photo path = %q", alex.PhotoPath)
	}
	if alex.Legacy {
		t.Fatal("frontmatter person should not be legacy")
	}
	maria := response.People[1]
	if maria.Name != "Мария Пример" || !maria.Legacy {
		t.Fatalf("unexpected legacy person: %+v", maria)
	}
	if len(maria.Groups) == 0 || maria.Groups[0] != "коллеги" || maria.Importance != 6 {
		t.Fatalf("legacy folder metadata was not inferred: %+v", maria)
	}
}

func TestListProjectPeopleUsesConfiguredDirectoryRecursively(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	root := t.TempDir()
	configuredRoot := filepath.Join(root, "Knowledge", "Contacts")
	if err := os.MkdirAll(filepath.Join(configuredRoot, "Friends", "Close"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "People"), 0o755); err != nil {
		t.Fatal(err)
	}
	personCard := "---\ntype: person\nname: Nested Person\nimportance: 7\n---\n"
	if err := os.WriteFile(filepath.Join(configuredRoot, "Friends", "Close", "Nested Person.md"), []byte(personCard), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideCard := "---\ntype: person\nname: Outside Person\nimportance: 10\n---\n"
	if err := os.WriteFile(filepath.Join(root, "People", "Outside Person.md"), []byte(outsideCard), 0o644); err != nil {
		t.Fatal(err)
	}
	project := createTestProject(t, store, "people-project", "People", root)
	project.Settings = map[string]string{peopleDirectorySetting: "Knowledge/Contacts"}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/projects/people-project/people", nil)
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
	if response.Directory != "Knowledge/Contacts" {
		t.Fatalf("directory = %q, want Knowledge/Contacts", response.Directory)
	}
	if len(response.People) != 1 || response.People[0].Name != "Nested Person" {
		t.Fatalf("configured recursive people = %+v", response.People)
	}
	if response.People[0].Path != "Knowledge/Contacts/Friends/Close/Nested Person.md" {
		t.Fatalf("path = %q", response.People[0].Path)
	}
}

func TestSaveProjectPersonPreservesMarkdownAndUnknownFrontmatter(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	root := t.TempDir()
	path := filepath.Join(root, "08-Люди", "3 - друзья", "Alex Example.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `---
type: person
name: Old Name
importance: 3
custom_field: keep-me
---

# Private notes

Keep this body exactly.
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	project := createTestProject(t, store, "people-project", "People", root)
	body := `{
		"path":"08-Люди/3 - друзья/Alex Example.md",
		"name":"Alex Example",
		"groups":["Friends","Founders"],
		"importance":9,
		"relationship":"friend",
		"company":"A2gent",
		"role":"Founder",
		"location":"Tallinn",
		"birthday":"1990-02-03",
		"last_contacted":"2026-08-20",
		"phones":["+372 555 0101"],
		"emails":["alex@example.com"],
		"socials":{"telegram":"https://t.me/alex"},
		"interests":["AI"],
		"traits":["reliable"],
		"aliases":["Sasha"],
		"tags":["close"],
		"status":"active"
	}`

	req := httptest.NewRequest(http.MethodPut, "/projects/people-project/people", strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("projectID", project.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	server.handleSaveProjectPerson(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(saved)
	for _, expected := range []string{
		"type: person", "name: Alex Example", "importance: 9", "custom_field: keep-me",
		"# Private notes\n\nKeep this body exactly.",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("saved card missing %q:\n%s", expected, content)
		}
	}
}

func TestCreateProjectPersonUsesPeopleDirectoryAndSafeFilename(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "08-Люди"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := createTestProject(t, store, "people-project", "People", root)

	req := httptest.NewRequest(http.MethodPost, "/projects/people-project/people", strings.NewReader(`{
		"name":"Jane / Example",
		"groups":["Friends"],
		"importance":7,
		"relationship":"friend"
	}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("projectID", project.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	server.handleCreateProjectPerson(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var person projectPerson
	if err := json.Unmarshal(rec.Body.Bytes(), &person); err != nil {
		t.Fatal(err)
	}
	if person.Path != "08-Люди/Friends/Jane - Example.md" {
		t.Fatalf("path = %q", person.Path)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(person.Path))); err != nil {
		t.Fatalf("created card missing: %v", err)
	}
}
