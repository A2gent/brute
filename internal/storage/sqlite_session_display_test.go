package storage

import (
	"strings"
	"testing"
	"time"
)

func TestSessionDisplayReadsKeepCompressionStateInStorage(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := "selected"
	other := "other"
	for _, row := range []*Session{
		{ID: "selected", AgentID: "build", ProjectID: &project, Status: "idle", Messages: []Message{{ID: "message", Role: "user", Content: "hello", Timestamp: time.Now()}}, Metadata: map[string]interface{}{"context_compression_store": strings.Repeat("x", 1<<20), "provider": "test", "sub_agent_id": "child"}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "other", AgentID: "build", ProjectID: &other, Status: "idle", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	} {
		if err := store.SaveSession(row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListSessionsForDisplay(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "selected" {
		t.Fatalf("unexpected filtered rows: %#v", rows)
	}
	detail, err := store.GetSessionForDisplay("selected", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []*Session{rows[0], detail} {
		if _, ok := row.Metadata["context_compression_store"]; ok {
			t.Fatal("compression state leaked into display read")
		}
		if row.Metadata["provider"] != "test" || row.Metadata["sub_agent_id"] != "child" {
			t.Fatalf("display metadata lost: %#v", row.Metadata)
		}
	}
	metadataOnly, err := store.GetSessionForDisplay("selected", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadataOnly.Messages) != 0 || len(detail.Messages) != 1 {
		t.Fatal("metadata-only read loaded transcript or detail lost messages")
	}

	full, err := store.GetSession("selected")
	if err != nil {
		t.Fatal(err)
	}
	if full.Metadata["context_compression_store"] != strings.Repeat("x", 1<<20) {
		t.Fatal("runtime compression state changed")
	}
}

func BenchmarkSessionListDisplay(b *testing.B) {
	store, err := NewSQLiteStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	project := "selected"
	row := &Session{ID: "large", AgentID: "build", ProjectID: &project, Status: "idle", CreatedAt: time.Now(), UpdatedAt: time.Now(), Metadata: map[string]interface{}{"context_compression_store": strings.Repeat("x", 50<<20), "provider": "test"}}
	if err := store.SaveSession(row); err != nil {
		b.Fatal(err)
	}
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := store.ListSessions(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("display", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := store.ListSessionsForDisplay(project); err != nil {
				b.Fatal(err)
			}
		}
	})
}
