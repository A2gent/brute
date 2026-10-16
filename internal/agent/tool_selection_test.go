package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

type namedTool struct{ name string }

func (t namedTool) Name() string                   { return t.name }
func (t namedTool) Description() string            { return t.name }
func (t namedTool) Schema() map[string]interface{} { return map[string]interface{}{"type": "object"} }
func (t namedTool) Execute(context.Context, json.RawMessage) (*tools.Result, error) {
	return &tools.Result{Success: true}, nil
}

// fakeJev answers every noul question with yes for groups in `yes`, no otherwise.
func fakeJev(t *testing.T, status int, yes ...string) (*jev.Client, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var req jev.SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if !strings.Contains(req.StateString(), "open example.com") {
			t.Errorf("state = %q", req.StateString())
		}
		answers := map[string]jev.Answer{}
		for name, q := range req.Questions {
			if q.Type != "noul" {
				t.Errorf("question %s type %s", name, q.Type)
			}
			score := 0.05
			for _, y := range yes {
				if y == name {
					score = 0.9
				}
			}
			answers[name] = jev.Answer{Type: "noul", Noul: &score}
		}
		_ = json.NewEncoder(w).Encode(jev.SystemOneResponse{Answers: answers})
	}))
	t.Cleanup(server.Close)
	return jev.NewClient("key", "", server.URL+"/v1"), &calls
}

func newSelectionAgent(t *testing.T, client *jev.Client) *Agent {
	t.Helper()
	manager := tools.NewManager(t.TempDir())
	for _, name := range []string{"browser_chrome", "jira_query", "meshy_generate_3d", "custom_mcp_tool"} {
		manager.Register(namedTool{name})
	}
	return New(Config{ToolSelector: &ToolSelector{Client: client}}, nil, manager, nil)
}

func toolNames(a *Agent, sess *session.Session) map[string]bool {
	out := map[string]bool{}
	for _, def := range a.buildRequest(context.Background(), sess).Tools {
		out[def.Name] = true
	}
	return out
}

func TestToolSelectionStickyWithEscapeHatch(t *testing.T) {
	client, calls := fakeJev(t, http.StatusOK, "browser")
	ag := newSelectionAgent(t, client)
	sess := session.New("test")
	sess.AddUserMessage("open example.com and check the title")

	ag.ensureToolGroups(context.Background(), sess)
	names := toolNames(ag, sess)
	if !names["browser_chrome"] || names["jira_query"] || names["meshy_generate_3d"] {
		t.Fatalf("selection not applied: %v", names)
	}
	// Core and ungrouped (dynamic) tools always stay; the escape hatch is offered.
	if !names["read"] || !names["bash"] || !names["custom_mcp_tool"] || !names[tools.EnableToolsToolName] {
		t.Fatalf("core tools missing: %v", names)
	}

	// Sticky: a later prompt neither re-asks Jev nor changes the tool list.
	sess.AddAssistantMessage("done", nil)
	sess.AddUserMessage("now open example.com in a new tab")
	ag.ensureToolGroups(context.Background(), sess)
	if *calls != 1 {
		t.Fatalf("jev calls = %d, want 1", *calls)
	}

	// enable_tools result widens the set and is persisted in metadata.
	result, err := ag.toolManager.Execute(context.Background(), tools.EnableToolsToolName, json.RawMessage(`{"group":"integrations"}`))
	if err != nil || !result.Success {
		t.Fatalf("enable_tools: %+v %v", result, err)
	}
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "c1", Name: tools.EnableToolsToolName, Input: json.RawMessage(`{"group":"integrations"}`)}})
	sess.AddToolResult([]session.ToolResult{{ToolCallID: "c1", Name: tools.EnableToolsToolName, Content: result.Output, Metadata: result.Metadata}})
	names = toolNames(ag, sess)
	if !names["jira_query"] || names["meshy_generate_3d"] {
		t.Fatalf("enable_tools not applied: %v", names)
	}
	if got := stringList(sess.Metadata[metadataToolGroups]); strings.Join(got, ",") != "browser,integrations" {
		t.Fatalf("stored groups = %v", got)
	}
}

func TestToolSelectionFailsOpen(t *testing.T) {
	client, _ := fakeJev(t, http.StatusInternalServerError)
	ag := newSelectionAgent(t, client)
	sess := session.New("test")
	sess.AddUserMessage("open example.com")
	ag.ensureToolGroups(context.Background(), sess)
	names := toolNames(ag, sess)
	if !names["browser_chrome"] || !names["jira_query"] || !names["meshy_generate_3d"] || names[tools.EnableToolsToolName] {
		t.Fatalf("fail-open should expose all tools without enable_tools: %v", names)
	}
}

func TestToolSelectionSkipsSecretsAndLegacySessions(t *testing.T) {
	client, calls := fakeJev(t, http.StatusOK)
	ag := newSelectionAgent(t, client)

	secret := session.New("test")
	secret.AddUserMessage("open example.com with api_key=sk-abcdefghijklmnopqrstuv")
	ag.ensureToolGroups(context.Background(), secret)
	if !toolNames(ag, secret)["jira_query"] {
		t.Fatal("secret prompt must fail open")
	}

	legacy := session.New("test")
	legacy.AddUserMessage("open example.com")
	legacy.AddAssistantMessage("ok", nil)
	ag.ensureToolGroups(context.Background(), legacy)
	if _, ok := legacy.Metadata[metadataToolGroups]; ok || !toolNames(ag, legacy)["jira_query"] {
		t.Fatal("legacy session must keep all tools")
	}
	if *calls != 0 {
		t.Fatalf("jev calls = %d, want 0", *calls)
	}
}

func TestToolSelectionDisabledKeepsAllTools(t *testing.T) {
	manager := tools.NewManager(t.TempDir())
	manager.Register(namedTool{"jira_query"})
	ag := New(Config{}, nil, manager, nil)
	sess := session.New("test")
	sess.AddUserMessage("hi")
	ag.ensureToolGroups(context.Background(), sess)
	names := toolNames(ag, sess)
	if !names["jira_query"] || names[tools.EnableToolsToolName] {
		t.Fatalf("disabled selector changed tools: %v", names)
	}
}
