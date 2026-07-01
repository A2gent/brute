package storage

import (
	"context"
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
