package storage

import (
	"testing"
	"time"
)

func TestSQLiteTeamsCRUDRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Second)
	record := &TeamRecord{
		ID:             "squad-product",
		ProjectID:      "project-a",
		Name:           "Product squad",
		Description:    "Build and review.",
		DefinitionYAML: "id: squad-product\nname: Product squad\n",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.SaveTeam(record); err != nil {
		t.Fatalf("SaveTeam() error = %v", err)
	}

	got, err := store.GetTeam(record.ID)
	if err != nil {
		t.Fatalf("GetTeam() error = %v", err)
	}
	if got.ID != record.ID || got.ProjectID != record.ProjectID || got.DefinitionYAML != record.DefinitionYAML || !got.CreatedAt.Equal(now) {
		t.Fatalf("GetTeam() = %#v, want %#v", got, record)
	}

	projectID := "project-a"
	listed, err := store.ListTeams(&projectID)
	if err != nil {
		t.Fatalf("ListTeams(project) error = %v", err)
	}
	if len(listed) != 1 || listed[0].ID != record.ID {
		t.Fatalf("ListTeams(project) = %#v", listed)
	}
	otherProject := "project-b"
	listed, err = store.ListTeams(&otherProject)
	if err != nil {
		t.Fatalf("ListTeams(other project) error = %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("ListTeams(other project) = %#v, want empty", listed)
	}

	record.Name = "Updated squad"
	record.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveTeam(record); err != nil {
		t.Fatalf("SaveTeam(update) error = %v", err)
	}
	got, err = store.GetTeam(record.ID)
	if err != nil {
		t.Fatalf("GetTeam(update) error = %v", err)
	}
	if got.Name != "Updated squad" || !got.CreatedAt.Equal(now) {
		t.Fatalf("updated team = %#v", got)
	}

	if err := store.DeleteTeam(record.ID); err != nil {
		t.Fatalf("DeleteTeam() error = %v", err)
	}
	if _, err := store.GetTeam(record.ID); err == nil {
		t.Fatal("GetTeam() after delete returned no error")
	}
}
