package storage

import "testing"

func TestMigrationDropsLegacyTeamTables(t *testing.T) {
	path := t.TempDir()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TABLE teams (id TEXT PRIMARY KEY);
		CREATE TABLE team_runs (id TEXT PRIMARY KEY);
		CREATE TABLE team_run_members (team_run_id TEXT NOT NULL REFERENCES team_runs(id) ON DELETE CASCADE);
		CREATE TABLE team_messages (id TEXT PRIMARY KEY, team_run_id TEXT NOT NULL REFERENCES team_runs(id) ON DELETE CASCADE);
		INSERT INTO team_runs (id) VALUES ('run');
		INSERT INTO team_run_members (team_run_id) VALUES ('run');
		INSERT INTO team_messages (id, team_run_id) VALUES ('msg', 'run')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{"teams", "team_runs", "team_run_members", "team_messages"} {
		exists, err := store.tableExists(table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("table %s still exists after migration", table)
		}
	}
}
