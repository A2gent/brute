package storage

import (
	"testing"
	"time"
)

func TestIntegrationProjectAndSubAgentFlagRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	now := time.Now()

	if err := store.SaveIntegration(&Integration{ID: "i1", Provider: "jira", Name: "J", Mode: "notify_only", ProjectID: "p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("SaveIntegration: %v", err)
	}
	got, err := store.GetIntegration("i1")
	if err != nil || got.ProjectID != "p1" {
		t.Fatalf("GetIntegration project_id = %+v, err=%v", got, err)
	}
	list, err := store.ListIntegrations()
	if err != nil || len(list) != 1 || list[0].ProjectID != "p1" {
		t.Fatalf("ListIntegrations = %+v, err=%v", list, err)
	}
	got.ProjectID = ""
	if err := store.SaveIntegration(got); err != nil {
		t.Fatalf("SaveIntegration update: %v", err)
	}
	if got, _ = store.GetIntegration("i1"); got.ProjectID != "" {
		t.Fatalf("expected integration to become global, got %q", got.ProjectID)
	}

	if err := store.SaveSubAgent(&SubAgent{ID: "sa1", Name: "A", Provider: "anthropic", ProjectIntegrationsOnly: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("SaveSubAgent: %v", err)
	}
	sa, err := store.GetSubAgent("sa1")
	if err != nil || !sa.ProjectIntegrationsOnly {
		t.Fatalf("GetSubAgent = %+v, err=%v", sa, err)
	}
	agents, err := store.ListSubAgents()
	if err != nil {
		t.Fatalf("ListSubAgents: %v", err)
	}
	for _, item := range agents {
		if item.ID == "sa1" && !item.ProjectIntegrationsOnly {
			t.Fatalf("ListSubAgents lost project_integrations_only")
		}
	}
}
