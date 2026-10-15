package storage

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Run with -benchtime=3x so WAL growth is comparable without auto-checkpoints.
func BenchmarkSaveSessionLargeCompression(b *testing.B) {
	store, err := NewSQLiteStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	row := &Session{ID: "large-save", AgentID: "build", Status: "idle", CreatedAt: time.Now(), UpdatedAt: time.Now(), Metadata: map[string]interface{}{"provider": "test", "context_compression_store": map[string]interface{}{"hash": map[string]interface{}{"hash": "hash", "original": strings.Repeat("x", 50<<20)}}}}
	if err := store.SaveSession(row); err != nil {
		b.Fatal(err)
	}
	if err := store.SaveCompressionEntries(row.ID, []CompressionEntry{{Hash: "hash", Original: strings.Repeat("x", 50<<20)}}); err != nil {
		b.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		b.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		row.UpdatedAt = time.Now()
		if err := store.SaveSession(row); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	info, err := os.Stat(store.dbPath + "-wal")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(info.Size())/float64(b.N), "WAL-B/op")
}
