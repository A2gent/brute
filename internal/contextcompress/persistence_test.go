package contextcompress

import (
	"context"
	"fmt"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

type compressionPersistenceSpy struct {
	entries map[string]storage.CompressionEntry
	reads   int
	fail    bool
}

func (s *compressionPersistenceSpy) SaveCompressionEntries(id string, entries []storage.CompressionEntry) error {
	if s.fail {
		return fmt.Errorf("disk full")
	}
	for _, entry := range entries {
		s.entries[id+":"+entry.Hash] = entry
	}
	return nil
}

func (s *compressionPersistenceSpy) GetCompressionEntry(id, hash string) (storage.CompressionEntry, error) {
	s.reads++
	value, ok := s.entries[id+":"+hash]
	if !ok {
		return value, fmt.Errorf("not found")
	}
	return value, nil
}

func TestCompressionLazyRetrieval(t *testing.T) {
	backing := &compressionPersistenceSpy{entries: make(map[string]storage.CompressionEntry)}
	sess := session.New("test")
	compressor := NewCompressorWithSessionStore(Config{}, backing)
	original := largeQueryableOutput()
	compressor.AdmitToolResult(sess, llm.ToolResult{Name: "fetch_url", ToolCallID: "call", Content: original}, 256)
	if len(sess.Metadata) != 0 || backing.reads != 0 || len(backing.entries) != 1 {
		t.Fatal("admission loaded session data or stored originals in metadata")
	}
	fresh := NewCompressorWithSessionStore(Config{}, backing)
	for _, entry := range backing.entries {
		for i := 0; i < 2; i++ {
			got, ok := fresh.Retrieve(sess.ID, entry.Hash, "")
			if !ok || got != original {
				t.Fatal("original lost")
			}
		}
	}
	if backing.reads != 1 {
		t.Fatalf("expected one lazy lookup with cache, got %d", backing.reads)
	}
}

func TestCompressionPersistenceFailureKeepsOriginal(t *testing.T) {
	backing := &compressionPersistenceSpy{fail: true}
	compressor := NewCompressorWithSessionStore(Config{Enabled: true}, backing)
	sess := session.New("test")
	original := largeQueryableOutput()
	tr := llm.ToolResult{Name: "fetch_url", ToolCallID: "call", Content: original}
	if got := compressor.AdmitToolResult(sess, tr, 256); got.Content != original {
		t.Fatal("failed write replaced original with marker")
	}
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{tr}}}}
	got, result := compressor.CompressRequest(context.Background(), sess.ID, req)
	if result.Applied || got.Messages[0].ToolResults[0].Content != original {
		t.Fatal("failed request-time write replaced original")
	}
}

func TestCommandAndJevAdmissionPersistence(t *testing.T) {
	for _, kind := range []string{"command", "jev"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%v", kind, fail), func(t *testing.T) {
				backing := &compressionPersistenceSpy{entries: make(map[string]storage.CompressionEntry), fail: fail}
				compressor := NewCompressorWithSessionStore(Config{}, backing)
				sess := gateSession()
				original := numberedOutput(600, func(i int) string {
					if i == 300 {
						return "RELEVANT failure"
					}
					return ""
				})
				tr := llm.ToolResult{Name: "bash", ToolCallID: "call", Content: original}
				var got llm.ToolResult
				if kind == "jev" {
					client, _, _ := fakeJev(t, .95, 200)
					got = compressor.AdmitToolResultWithGate(context.Background(), sess, tr, 8000, &RelevanceGate{Client: client})
				} else {
					got = compressor.AdmitToolResult(sess, tr, 8000)
				}
				if len(sess.Metadata) != 0 {
					t.Fatal("admission put originals in metadata")
				}
				if fail {
					if got.Content != original {
						t.Fatal("failed persistence lost original")
					}
					return
				}
				if got.Content == original || len(backing.entries) != 1 {
					t.Fatal("admission did not persist preview")
				}
				fresh := NewCompressorWithSessionStore(Config{}, backing)
				for _, entry := range backing.entries {
					if full, ok := fresh.Retrieve(sess.ID, entry.Hash, ""); !ok || full != original {
						t.Fatal("fresh compressor cannot retrieve original")
					}
				}
			})
		}
	}
}
