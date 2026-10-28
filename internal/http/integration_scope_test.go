package http

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

// A delegated token is bound to the parent session, but integration tools must
// see the child agent's scope (its project + project-only flag), not the parent's.
func TestBridgeDelegatedTokenScopeOverridesParentSessionScope(t *testing.T) {
	parent := newMCPBridgeTestServer(t)
	now := time.Now()
	for _, id := range []string{"parent-proj", "child-proj"} {
		if err := parent.store.SaveProject(&storage.Project{ID: id, Name: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	parentSess, _ := parent.sessionManager.Create("build")
	parentProject := "parent-proj"
	parentSess.ProjectID = &parentProject
	if err := parent.sessionManager.Save(parentSess); err != nil {
		t.Fatal(err)
	}

	var seen []integrationtools.Scope
	parent.toolManager.Register(&bridgeDelegationProbe{name: "jira_query", execute: func(ctx context.Context, _ json.RawMessage) (*tools.Result, error) {
		scope, ok := integrationtools.ScopeFromContext(ctx)
		if !ok {
			t.Error("bridge tool call has no integration scope")
		}
		seen = append(seen, scope)
		return &tools.Result{Success: true, Output: "ok"}, nil
	}})
	call := func(token string) {
		t.Helper()
		sessionID, tokenCtx, ok := parent.mcpBridge.resolve(token)
		if !ok {
			t.Fatal("token did not resolve")
		}
		sess, err := parent.sessionManager.Get(sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if text, failed, _, rpcErr := parent.mcpBridgeCallToolResult(tokenCtx, sess, json.RawMessage(`{"name":"jira_query"}`)); failed || rpcErr != nil {
			t.Fatalf("tool call failed: %s %+v", text, rpcErr)
		}
	}

	workspace := &dockerWorkspaceBinding{ProjectID: "child-proj", ProjectIntegrationsOnly: true}
	headers, revoke, err := parent.delegatedAgentToolsHeaders(t.Context(), parentSess.ID, agentdef.RuntimeDocker, workspace)
	if err != nil || headers.Get(parentAgentToolsHeader) == "" {
		t.Fatalf("delegated child got no capability: %v %v", headers, err)
	}
	defer revoke()
	call(headers.Get(parentAgentToolsHeader))

	plain, revokePlain, err := parent.mcpBridge.mint(parentSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer revokePlain()
	call(plain)

	want := []integrationtools.Scope{{ProjectID: "child-proj", ProjectOnly: true}, {ProjectID: "parent-proj"}}
	if len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("scopes = %+v, want %+v", seen, want)
	}
}
