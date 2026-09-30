package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

type bridgeDelegationProbe struct {
	name    string
	execute func(context.Context, json.RawMessage) (*tools.Result, error)
}

func (p *bridgeDelegationProbe) Name() string        { return p.name }
func (p *bridgeDelegationProbe) Description() string { return "delegation probe" }
func (p *bridgeDelegationProbe) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (p *bridgeDelegationProbe) Execute(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	return p.execute(ctx, args)
}

func TestMCPBridgeDelegationRoundTrip(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	token, revoke := s.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()
	for _, name := range []string{"delegate_to_agent", "delegate_to_subagent", "delegate_to_external_agent"} {
		t.Run(name, func(t *testing.T) {
			response := strings.Repeat("review context ✓ ", 600)
			output, _ := json.Marshal(map[string]interface{}{"child_session_id": "child-1", "response": response})
			s.toolManager.Register(&bridgeDelegationProbe{name: name, execute: func(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
				if ctx.Value("session_id") != sess.ID {
					t.Error("lost parent session")
				}
				var task struct {
					Task string `json:"task"`
				}
				_ = json.Unmarshal(args, &task)
				if task.Task != "review the supplied context" {
					t.Errorf("lost task: %s", args)
				}
				return &tools.Result{Success: true, Output: string(output)}, nil
			}})
			code, resp := serveMCPBridge(t, s, newMCPBridgeRequest(t, sess.ID, token, mcpBridgeRPC(t, 1, "tools/call", map[string]interface{}{"name": name, "arguments": map[string]string{"task": "review the supplied context"}})))
			text, failed := mcpBridgeCallResultText(t, resp)
			if code != http.StatusOK || failed || text != string(output) {
				t.Fatalf("round trip failed: %d %v %s", code, failed, text)
			}
		})
	}
}

func TestDelegatedSessionCannotDelegate(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	for _, metadata := range []map[string]interface{}{
		{"source": "delegate_to_agent", "parent_session_id": "remote-parent"},
		{"sub_agent_id": "legacy-child"},
	} {
		sess, _ := s.sessionManager.Create("build")
		sess.Metadata = metadata
		if err := s.sessionManager.Save(sess); err != nil {
			t.Fatal(err)
		}
		manager := s.toolManagerForSession(sess)
		for _, name := range []string{"delegate_to_agent", "delegate_to_subagent", "delegate_to_external_agent"} {
			if _, ok := manager.Get(name); ok {
				t.Errorf("child can still call %s", name)
			}
			args, _ := json.Marshal(map[string]interface{}{"name": name, "arguments": map[string]string{"agent_id": "reviewer", "task": "review"}})
			text, failed, _ := s.mcpBridgeCallTool(context.Background(), sess, args)
			if !failed {
				t.Errorf("direct bridge bypass: %s", text)
			}
		}
	}
	if _, ok := s.toolManagerForSession(&session.Session{}).Get("delegate_to_agent"); !ok {
		t.Fatal("child filtering mutated global manager")
	}
}

func TestMCPBridgeDisabledQuestionCannotBeCalled(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	settings, _ := s.store.GetSettings()
	settings[disabledToolsSettingKey] = `["question","delegate_to_agent"]`
	if err := s.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s.mcpBridge.questionTimeout = time.Millisecond
	sess, _ := s.sessionManager.Create("build")
	for _, name := range []string{"question", "delegate_to_agent"} {
		args, _ := json.Marshal(map[string]interface{}{"name": name, "arguments": map[string]string{"question": "bypass?"}})
		text, failed, _ := s.mcpBridgeCallTool(context.Background(), sess, args)
		if !failed || !strings.Contains(text, "tool not found") {
			t.Errorf("disabled tool %s executed: %s", name, text)
		}
	}
}

func TestMCPBridgeInvocationCancellation(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config, revoke, err := s.claudecliMCPBridgeHook(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	var cfg struct {
		MCPServers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(cfg.MCPServers["a2gent"].Headers["Authorization"], "Bearer ")
	_, tokenCtx, ok := s.mcpBridge.resolve(token)
	if !ok {
		t.Fatal("missing token")
	}
	cancel()
	select {
	case <-tokenCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("token context survives parent cancellation")
	}
}
