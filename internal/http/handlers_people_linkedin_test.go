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

func TestCanonicalLinkedInURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://www.linkedin.com/in/Anna-K/":                   "https://www.linkedin.com/in/anna-k/",
		"https://linkedin.com/in/anna-k?miniProfileUrn=x":       "https://www.linkedin.com/in/anna-k/",
		"https://ee.linkedin.com/in/anna-k/details/experience/": "https://www.linkedin.com/in/anna-k/",
	} {
		if got, err := canonicalLinkedInURL(raw); err != nil || got != want {
			t.Fatalf("canonicalLinkedInURL(%q) = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"https://evil.com/in/anna", "https://www.linkedin.com/company/acme/", ""} {
		if _, err := canonicalLinkedInURL(raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

func TestReplaceLinkedInSection(t *testing.T) {
	body := "# Anna\n\nNotes.\n\n## LinkedIn\nold\n\n### About\nold about\n\n## Meetings\n- coffee\n"
	got := replaceLinkedInSection(body, "## LinkedIn\nnew\n")
	want := "# Anna\n\nNotes.\n\n## LinkedIn\nnew\n\n## Meetings\n- coffee\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	if got := replaceLinkedInSection("# Anna\n", "## LinkedIn\nnew\n"); got != "# Anna\n\n## LinkedIn\nnew\n" {
		t.Fatalf("append got %q", got)
	}
}

func TestSyncProjectPersonLinkedIn(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	root := t.TempDir()
	peopleRoot := filepath.Join(root, "People")
	if err := os.MkdirAll(peopleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cards := map[string]string{
		"Anna.md":  "---\ntype: person\nname: Anna\nrole: Founder\nsocials:\n  telegram: '@anna'\n---\n\n# Anna\n\nNotes stay.\n",
		"Boris.md": "---\ntype: person\nname: Boris\nsocials:\n  linkedin: https://linkedin.com/in/anna-k\n---\n\n# Boris\n",
	}
	for name, content := range cards {
		if err := os.WriteFile(filepath.Join(peopleRoot, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	project := createTestProject(t, store, "li-sync-project", "LinkedIn sync", root)

	sync := func(body personLinkedInSyncRequest) (int, projectPerson) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/projects/x/people/linkedin-sync", bytes.NewReader(raw))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("projectID", project.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		rec := httptest.NewRecorder()
		server.handleSyncProjectPersonLinkedIn(rec, req)
		var person projectPerson
		_ = json.Unmarshal(rec.Body.Bytes(), &person)
		return rec.Code, person
	}

	profile := linkedInProfile{
		URL: "https://www.linkedin.com/in/Anna-K/", Name: "Anna K", Headline: "CTO at Acme",
		Role: "CTO", Company: "Acme", Location: "Tallinn", About: "Builds things.",
		Experience: []string{"CTO - Acme (2020 - Present)"}, Education: []string{"TalTech"},
	}
	code, person := sync(personLinkedInSyncRequest{Path: "People/Anna.md", Profile: profile})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	// Manual role wins; empty company/location are filled; existing socials are kept.
	if person.Role != "Founder" || person.Company != "Acme" || person.Location != "Tallinn" {
		t.Fatalf("person = %#v", person)
	}
	if person.Socials["linkedin"] != "https://www.linkedin.com/in/anna-k/" || person.Socials["telegram"] != "@anna" {
		t.Fatalf("socials = %#v", person.Socials)
	}
	annaRaw, _ := os.ReadFile(filepath.Join(peopleRoot, "Anna.md"))
	for _, want := range []string{"Notes stay.", "## LinkedIn", "**Headline**: CTO at Acme", "### About\nBuilds things.", "- CTO - Acme (2020 - Present)"} {
		if !strings.Contains(string(annaRaw), want) {
			t.Fatalf("missing %q in:\n%s", want, annaRaw)
		}
	}
	borisRaw, _ := os.ReadFile(filepath.Join(peopleRoot, "Boris.md"))
	if strings.Contains(string(borisRaw), "linkedin") {
		t.Fatalf("profile should be unlinked from Boris:\n%s", borisRaw)
	}

	// Re-sync rewrites the section instead of appending a second one, even after heading-like about text.
	profile.About = "## Not a heading"
	if code, _ := sync(personLinkedInSyncRequest{Path: "People/Anna.md", Profile: profile}); code != http.StatusOK {
		t.Fatalf("resync got %d", code)
	}
	profile.About = "Ships things."
	if code, _ := sync(personLinkedInSyncRequest{Path: "People/Anna.md", Profile: profile}); code != http.StatusOK {
		t.Fatalf("resync got %d", code)
	}
	annaRaw, _ = os.ReadFile(filepath.Join(peopleRoot, "Anna.md"))
	if strings.Count(string(annaRaw), "## LinkedIn") != 1 || strings.Contains(string(annaRaw), "Builds things.") || strings.Contains(string(annaRaw), "Not a heading") {
		t.Fatalf("section not replaced:\n%s", annaRaw)
	}

	if code, _ := sync(personLinkedInSyncRequest{Path: "../outside.md", Profile: profile}); code != http.StatusBadRequest {
		t.Fatalf("expected 400 for path outside people dir, got %d", code)
	}
}
