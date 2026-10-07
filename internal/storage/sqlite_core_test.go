package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestNewSQLiteStoreWithCurrentSchemaDoesNotWriteUnderBusyDatabase(t *testing.T) {
	dataPath := t.TempDir()
	store, err := NewSQLiteStore(dataPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() = %v", err)
	}
	dbPath := store.dbPath
	if err := store.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	locker, err := openSQLiteConnection(dbPath)
	if err != nil {
		t.Fatalf("openSQLiteConnection() = %v", err)
	}
	defer locker.Close()

	conn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn() = %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE = %v", err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	done := make(chan error, 1)
	go func() {
		reopened, err := NewSQLiteStore(dataPath)
		if err == nil {
			err = reopened.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("NewSQLiteStore = %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("NewSQLiteStore tried to write while database was locked")
	}
}

// Six active sessions must not monopolize the connection used by list/detail
// views. Keep a write transaction open to deterministically model a slow save.
func TestSessionViewsReadCommittedDataWhileWriterIsBusy(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	for _, id := range []string{"one", "two", "three", "four", "five", "six"} {
		if err := store.SaveSession(&Session{ID: id, AgentID: "build", Status: "running", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE sessions SET status = 'completed' WHERE id = 'one'"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() {
			rows, err := store.ListSessionsForDisplay("")
			if err == nil && len(rows) != 6 {
				err = fmt.Errorf("got %d sessions", len(rows))
			}
			if err == nil {
				var row *Session
				row, err = store.GetSessionForDisplay("one", true)
				if err == nil && row.Status != "running" {
					err = fmt.Errorf("read uncommitted status: %s", row.Status)
				}
			}
			done <- err
		}()
	}
	deadline := time.After(time.Second)
	for i := 0; i < 6; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("session views queued behind the writer")
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetSessionSummary("one")
	if err != nil || row.Status != "completed" {
		t.Fatalf("committed update not visible: row=%+v error=%v", row, err)
	}
}

func TestSessionReaderPoolRejectsWrites(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.sessionReader().Exec("DELETE FROM sessions"); err == nil {
		t.Fatal("reader connection accepted a write")
	}
}
