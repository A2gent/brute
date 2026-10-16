package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Run with -benchtime=10x: disable auto-checkpoints to measure WAL per save.
func BenchmarkSaveSessionSystemPromptSnapshot(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			store, err := NewSQLiteStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			snapshot := map[string]interface{}{"base_prompt": "frozen", "combined_prompt": strings.Repeat("x", size)}
			row := &Session{ID: "snapshot-bench", AgentID: "build", Status: "idle", CreatedAt: time.Now(), UpdatedAt: time.Now(), Metadata: map[string]interface{}{"provider": "test", "system_prompt_snapshot": snapshot}}
			if err := store.SaveSession(row); err != nil {
				b.Fatal(err)
			}
			// The same benchmark also runs against pre-migration storage.
			if snapshots, ok := interface{}(store).(interface {
				SaveSystemPromptSnapshot(string, json.RawMessage) error
			}); ok {
				raw, _ := json.Marshal(snapshot)
				if err := snapshots.SaveSystemPromptSnapshot(row.ID, raw); err != nil {
					b.Fatal(err)
				}
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
		})
	}
}
