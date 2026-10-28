package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionTemplateAgentMigrationRetainsLegacyPrompts(t *testing.T) {
	for _, withSlashCommand := range []bool{false, true} {
		name := "without slash command"
		if withSlashCommand {
			name = "with slash command"
		}
		t.Run(name, func(t *testing.T) {
			dataPath := t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(dataPath, "aagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			slashColumn := ""
			if withSlashCommand {
				slashColumn = "slash_command TEXT NOT NULL DEFAULT '',"
			}
			if _, err := db.Exec(`CREATE TABLE session_templates (
				id TEXT PRIMARY KEY, name TEXT NOT NULL, ` + slashColumn + `
				content TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
			)`); err != nil {
				t.Fatal(err)
			}
			created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
			updated := created.Add(time.Hour)
			for _, id := range []string{"legacy-custom", "built-in:branch-review:quality"} {
				if _, err := db.Exec(`INSERT INTO session_templates (id, name, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, id, "Legacy name", "Legacy prompt", created, updated); err != nil {
					t.Fatal(err)
				}
			}
			if withSlashCommand {
				if _, err := db.Exec(`UPDATE session_templates SET slash_command = id`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE UNIQUE INDEX idx_session_templates_slash_command ON session_templates(slash_command COLLATE NOCASE) WHERE slash_command <> ''`); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			// Reopen twice to cover both migration of old databases and idempotent startup.
			for i := 0; i < 2; i++ {
				store, err := NewSQLiteStore(dataPath)
				if err != nil {
					t.Fatal(err)
				}
				templates, err := store.ListSessionTemplates()
				if err != nil {
					store.Close()
					t.Fatal(err)
				}
				if len(templates) != 2 {
					store.Close()
					t.Fatalf("unexpected templates: %+v", templates)
				}
				for _, template := range templates {
					fetched, err := store.GetSessionTemplate(template.ID)
					if err != nil || fetched.Name != "Legacy name" || fetched.Content != "Legacy prompt" || fetched.AgentID != "" || !fetched.CreatedAt.Equal(created) || !fetched.UpdatedAt.Equal(updated) {
						store.Close()
						t.Fatalf("legacy prompt changed: %+v, %v", fetched, err)
					}
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
