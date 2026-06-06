package http

import (
	"strings"
	"testing"
)

func TestWorkflowNodeChildSessionReusesPreviousRuntimeState(t *testing.T) {
	previous := &workflowRuntimeState{
		Nodes: map[string]*workflowRuntimeNodeState{
			"developer": {ChildSessionID: "child-dev"},
		},
	}
	def := &workflowDefinitionRuntime{
		Nodes: []workflowNodeRuntime{
			{ID: "user", Kind: "user"},
			{ID: "developer", Kind: "subagent"},
		},
	}

	next := &workflowRuntimeState{Nodes: make(map[string]*workflowRuntimeNodeState, len(def.Nodes))}
	for _, node := range def.Nodes {
		st := &workflowRuntimeNodeState{Status: "pending"}
		if previous != nil && previous.Nodes != nil {
			if previousNodeState := previous.Nodes[node.ID]; previousNodeState != nil {
				st.ChildSessionID = strings.TrimSpace(previousNodeState.ChildSessionID)
			}
		}
		next.Nodes[node.ID] = st
	}

	if got := next.Nodes["developer"].ChildSessionID; got != "child-dev" {
		t.Fatalf("expected developer child session reuse, got %q", got)
	}
	if got := next.Nodes["user"].ChildSessionID; got != "" {
		t.Fatalf("expected user node to have no child session, got %q", got)
	}
}
