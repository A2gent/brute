package storage

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/imagefiles"
)

func legacyImageStore(t *testing.T) (*SQLiteStore, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewSQLiteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	_, _ = rand.New(rand.NewSource(1)).Read(img.Pix)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(buf.Bytes())
	sess := &Session{ID: "session", AgentID: "build", Status: "paused", CreatedAt: time.Now(), UpdatedAt: time.Now(), Messages: []Message{{ID: "message", Role: "user", Content: "old screenshot", Metadata: map[string]interface{}{"keep": "value"}, Timestamp: time.Now()}}}
	if err := store.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"keep": "value", "images": []map[string]interface{}{{"name": "old.png", "media_type": "image/png", "data_base64": data, "custom": "preserve"}, {"url": "https://example.com/image.png"}}})
	if _, err := store.db.Exec("UPDATE messages SET metadata = ? WHERE id = 'message'", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	return store, root, data
}

func TestMigrateSessionImagesBackupRoundtripAndVacuum(t *testing.T) {
	store, root, data := legacyImageStore(t)
	queued, _ := json.Marshal([]map[string]string{{"media_type": "image/png", "data_base64": data}})
	if _, err := store.db.Exec("INSERT INTO session_pending_messages (id,session_id,content,images,created_at) VALUES (?,?,?,?,?)", "pending", "session", "queued", string(queued), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "before.db")
	result, err := MigrateSessionImages(root, backup)
	if err != nil {
		t.Fatal(err)
	}
	if result.Messages != 1 || result.PendingMessages != 1 || result.AfterBytes >= result.BeforeBytes {
		t.Fatalf("migration did not shrink database: %+v", result)
	}
	old, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var oldData string
	if err := old.QueryRow("SELECT json_extract(metadata, '$.images[0].data_base64') FROM messages").Scan(&oldData); err != nil || oldData != data {
		t.Fatalf("backup missing original: %v", err)
	}
	migrated, err := sql.Open("sqlite", filepath.Join(root, "aagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var queuedPayload string
	if err := migrated.QueryRow("SELECT images FROM session_pending_messages").Scan(&queuedPayload); err != nil || strings.Contains(queuedPayload, "data_base64") || !strings.Contains(queuedPayload, "sha256") {
		t.Fatalf("pending images not migrated: %s %v", queuedPayload, err)
	}
	var payload []byte
	if err := migrated.QueryRow("SELECT metadata FROM messages").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Keep   string                  `json:"keep"`
		Images []imagefiles.Attachment `json:"images"`
	}
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Keep != "value" || metadata.Images[1].URL == "" || bytes.Contains(payload, []byte("data_base64")) || !bytes.Contains(payload, []byte("preserve")) {
		t.Fatalf("metadata lost or inline: %s", payload)
	}
	metadata.Images[0].Root = root
	inline, err := metadata.Images[0].Inline()
	if err != nil || inline.DataBase64 != data {
		t.Fatalf("file roundtrip: %v", err)
	}
	second, err := MigrateSessionImages(root, filepath.Join(root, "second.db"))
	if err != nil || second.Messages != 0 {
		t.Fatalf("migration not idempotent: %+v %v", second, err)
	}
	if _, err := MigrateSessionImages(root, backup); err == nil {
		t.Fatal("overwrote backup")
	}
}

func TestMigrateSessionImagesFailurePreservesOriginal(t *testing.T) {
	store, root, _ := legacyImageStore(t)
	if _, err := store.db.Exec(`UPDATE messages SET metadata = '{"images":[{"media_type":"image/png","data_base64":"broken"}]}'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateSessionImages(root, filepath.Join(root, "before.db")); err == nil {
		t.Fatal("expected extraction error")
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "aagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var data string
	if err := db.QueryRow("SELECT json_extract(metadata, '$.images[0].data_base64') FROM messages").Scan(&data); err != nil || data != "broken" {
		t.Fatalf("original changed: %s %v", data, err)
	}
}
