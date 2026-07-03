package storage

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// This file keeps SQLite connection/bootstrap concerns separate from repository CRUD methods.

// SQLiteStore implements Store using SQLite
type SQLiteStore struct {
	db       *sql.DB
	dataPath string
	dbPath   string
	mu       sync.Mutex
}

// NewSQLiteStore creates a new SQLite store
func NewSQLiteStore(dataPath string) (*SQLiteStore, error) {
	resolvedDataPath, err := filepath.Abs(dataPath)
	if err != nil {
		resolvedDataPath = dataPath
	}
	if err := os.MkdirAll(resolvedDataPath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}
	dbPath := filepath.Join(resolvedDataPath, "aagent.db")

	db, err := openSQLiteConnection(dbPath)
	if err != nil {
		return nil, err
	}

	store := &SQLiteStore{db: db, dataPath: resolvedDataPath, dbPath: dbPath}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return store, nil
}

func openSQLiteConnection(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	// SQLite supports one writer at a time. Keep pool size constrained to
	// reduce lock contention ("SQLITE_BUSY") under concurrent goroutines.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return db, nil
}

func sqliteDSN(dbPath string) string {
	values := url.Values{}
	values.Add("_pragma", "busy_timeout=30000")
	values.Add("_pragma", "foreign_keys=ON")
	values.Add("_pragma", "synchronous=NORMAL")

	u := url.URL{Scheme: "file", Path: filepath.ToSlash(dbPath)}
	u.RawQuery = values.Encode()
	return u.String()
}

func isSQLiteReadonlyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "readonly database") || strings.Contains(msg, "readonly")
}

func (s *SQLiteStore) reopenOnReadonly(writeErr error) error {
	if !isSQLiteReadonlyError(writeErr) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Another goroutine may have already swapped connections.
	if !isSQLiteReadonlyError(writeErr) {
		return nil
	}
	nextDB, err := openSQLiteConnection(s.dbPath)
	if err != nil {
		return fmt.Errorf("failed to reopen sqlite database after readonly error: %w", err)
	}
	prev := s.db
	s.db = nextDB
	if prev != nil {
		_ = prev.Close()
	}
	return nil
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func nullableString(value *string) interface{} {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func setNullableString(target **string, value sql.NullString) {
	if !value.Valid {
		*target = nil
		return
	}
	trimmed := strings.TrimSpace(value.String)
	if trimmed == "" {
		*target = nil
		return
	}
	*target = &trimmed
}

// Ensure SQLiteStore implements Store
var _ Store = (*SQLiteStore)(nil)
