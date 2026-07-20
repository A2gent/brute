package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestMergeFreshSessionStateApprovalAuditFreshWins(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "approval_audit_merge")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := storage.NewSQLiteStore(tmpDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("build")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	freshAudit := []map[string]interface{}{
		{"kind": "requested", "request_id": "req-fresh"},
		{"kind": "resolved", "request_id": "req-fresh", "decision": "allow_once"},
	}
	fresh, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fresh.Metadata == nil {
		fresh.Metadata = make(map[string]interface{})
	}
	fresh.Metadata["approval_audit"] = freshAudit
	fresh.Metadata["token_usage"] = map[string]interface{}{"in_flight": false}
	if err := sm.Save(fresh); err != nil {
		t.Fatalf("save fresh: %v", err)
	}

	inFlight := *sess
	inFlight.Metadata = map[string]interface{}{
		"approval_audit": []map[string]interface{}{
			{"kind": "requested", "request_id": "req-stale"},
		},
		"token_usage": map[string]interface{}{"in_flight": true},
	}

	ag := New(Config{}, &MockLLM{}, nil, sm)
	ag.mergeFreshSessionState(&inFlight)

	got, ok := inFlight.Metadata["approval_audit"]
	if !ok {
		t.Fatal("missing approval_audit after merge")
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "req-stale") {
		t.Fatalf("stale audit won: %s", data)
	}
	if !strings.Contains(string(data), "req-fresh") {
		t.Fatalf("fresh audit missing: %s", data)
	}

	tokenUsage, _ := inFlight.Metadata["token_usage"].(map[string]interface{})
	if tokenUsage["in_flight"] != true {
		t.Fatalf("in-flight metadata should win for non-audit keys: %#v", inFlight.Metadata["token_usage"])
	}
}
