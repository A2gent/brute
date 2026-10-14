package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/llm/jev"
)

func TestSelectToolGroupsMissingAnswerFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.SystemOneRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Questions) != len(ToolGroups) {
			t.Errorf("questions = %d, want one per group", len(req.Questions))
		}
		// Only one answer: partial data must not be trusted.
		_, _ = w.Write([]byte(`{"answers":{"browser":{"type":"noul","noul":0.9}}}`))
	}))
	defer server.Close()
	if _, err := SelectToolGroups(context.Background(), jev.NewClient("key", "", server.URL), "x", 0.3); err == nil {
		t.Fatal("expected error on missing answers")
	}
}

func TestEnableToolsRejectsUnknownGroup(t *testing.T) {
	result, err := NewEnableToolsTool().Execute(context.Background(), json.RawMessage(`{"group":"nope"}`))
	if err != nil || result.Success {
		t.Fatalf("unknown group accepted: %+v %v", result, err)
	}
	result, _ = NewEnableToolsTool().Execute(context.Background(), json.RawMessage(`{"group":"all"}`))
	if got := result.Metadata[EnableToolGroupsMetadataKey].([]string); len(got) != len(ToolGroups) {
		t.Fatalf("all enabled %v", got)
	}
}

func TestGetDefinitionsStableOrder(t *testing.T) {
	defs := NewManager(t.TempDir()).GetDefinitions()
	for i := 1; i < len(defs); i++ {
		if defs[i-1].Name > defs[i].Name {
			t.Fatalf("definitions not sorted at %d: %s > %s", i, defs[i-1].Name, defs[i].Name)
		}
	}
}

func TestSelectToolGroupsInvalidProbabilityFailsOpen(t *testing.T) {
	for _, value := range []string{`{"type":"score","noul":0.9}`, `{"type":"noul","noul":-1}`, `{"type":"noul","noul":1.5}`} {
		t.Run(value, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				answers := map[string]json.RawMessage{}
				for _, group := range ToolGroups {
					answers[group.Name] = json.RawMessage(value)
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"answers": answers})
			}))
			defer server.Close()
			if _, err := SelectToolGroups(context.Background(), jev.NewClient("key", "", server.URL), "task", 0.3); err == nil {
				t.Fatal("malformed probabilities must not hide tools")
			}
		})
	}
}
