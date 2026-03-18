package http

import "testing"

func TestParseFavoriteA2AAgents(t *testing.T) {
	raw := `[
		{"id":"agent-1","name":"Research"},
		{"id":"   ","name":"skip"},
		{"id":"agent-2","name":"  Code Reviewer  "}
	]`

	agents := parseFavoriteA2AAgents(raw)
	if len(agents) != 2 {
		t.Fatalf("expected 2 parsed agents, got %d", len(agents))
	}
	if agents[0].ID != "agent-1" || agents[0].Name != "Research" {
		t.Fatalf("unexpected first agent: %+v", agents[0])
	}
	if agents[1].ID != "agent-2" || agents[1].Name != "Code Reviewer" {
		t.Fatalf("unexpected second agent: %+v", agents[1])
	}
}

func TestResolveFavoriteExternalAgent(t *testing.T) {
	favorites := []favoriteA2AAgent{
		{ID: "agent-a", Name: "ResearchBot"},
		{ID: "agent-b", Name: "CodeReviewer"},
	}

	resolved, err := resolveFavoriteExternalAgent(favorites, "researchbot")
	if err != nil {
		t.Fatalf("expected exact name match, got error: %v", err)
	}
	if resolved == nil || resolved.ID != "agent-a" {
		t.Fatalf("unexpected resolved agent: %+v", resolved)
	}

	resolved, err = resolveFavoriteExternalAgent(favorites, "review")
	if err != nil {
		t.Fatalf("expected substring name match, got error: %v", err)
	}
	if resolved == nil || resolved.ID != "agent-b" {
		t.Fatalf("unexpected resolved agent for substring: %+v", resolved)
	}

	if _, err := resolveFavoriteExternalAgent(favorites, "missing"); err == nil {
		t.Fatalf("expected missing favorite to return an error")
	}
}
