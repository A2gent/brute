package http

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestAppendWorkflowTranscriptEntryPersistsMetadata(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	srv := &Server{sessionManager: session.NewManager(store)}
	sess := session.New("build")

	entry := workflowTranscriptEntry{
		ID:             "session:developer:1",
		NodeID:         "developer",
		NodeLabel:      "Developer",
		NodeKind:       "subagent",
		ChildSessionID: "child-1",
		Role:           "agent",
		Content:        "Implemented the feature.",
		CreatedAt:      "2026-05-14T20:00:00Z",
		Status:         "completed",
		Turn:           1,
	}

	if err := srv.appendWorkflowTranscriptEntry(sess, entry, nil); err != nil {
		t.Fatalf("append transcript entry: %v", err)
	}

	entries := workflowTranscriptEntriesFromMetadata(sess.Metadata[workflowTranscriptMetadataKey])
	if len(entries) != 1 {
		t.Fatalf("expected one transcript entry, got %d", len(entries))
	}
	if entries[0].ID != entry.ID || entries[0].Content != entry.Content || entries[0].NodeLabel != entry.NodeLabel {
		t.Fatalf("unexpected transcript entry: %+v", entries[0])
	}
}

func TestAppendWorkflowTranscriptEntryDeduplicatesByID(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	srv := &Server{sessionManager: session.NewManager(store)}
	sess := session.New("build")
	entry := workflowTranscriptEntry{
		ID:        "session:critic:1",
		NodeID:    "critic",
		NodeLabel: "Critic",
		Role:      "agent",
		Content:   "Approved.",
		CreatedAt: "2026-05-14T20:01:00Z",
		Status:    "completed",
		Turn:      1,
	}

	if err := srv.appendWorkflowTranscriptEntry(sess, entry, nil); err != nil {
		t.Fatalf("append transcript entry: %v", err)
	}
	if err := srv.appendWorkflowTranscriptEntry(sess, entry, nil); err != nil {
		t.Fatalf("append duplicate transcript entry: %v", err)
	}

	entries := workflowTranscriptEntriesFromMetadata(sess.Metadata[workflowTranscriptMetadataKey])
	if len(entries) != 1 {
		t.Fatalf("expected duplicate to be skipped, got %d entries", len(entries))
	}
}

func TestWorkflowTranscriptEntryStreamJSON(t *testing.T) {
	entry := workflowTranscriptEntry{
		ID:        "session:developer:1",
		NodeID:    "developer",
		NodeLabel: "Developer",
		Role:      "agent",
		Content:   "Done.",
		CreatedAt: "2026-05-14T20:02:00Z",
	}
	encoded, err := json.Marshal(ChatStreamEvent{Type: "workflow_transcript_entry", WorkflowTranscriptEntry: entry})
	if err != nil {
		t.Fatalf("marshal stream event: %v", err)
	}
	if !strings.Contains(string(encoded), "workflow_transcript_entry") || !strings.Contains(string(encoded), "Developer") {
		t.Fatalf("expected transcript entry in JSON, got %s", encoded)
	}
}

func TestWorkflowRuntimeStateFromMetadataRoundTripsStoredMap(t *testing.T) {
	sess := session.New("build")
	sess.Metadata = map[string]interface{}{
		workflowStateMetadataKey: map[string]interface{}{
			"workflowId":   "wf-review",
			"workflowName": "Review loop",
			"status":       "completed",
			"updatedAt":    "2026-05-14T00:00:00Z",
			"nodes": map[string]interface{}{
				"developer": map[string]interface{}{
					"status":         "completed",
					"childSessionId": "child-dev",
				},
				"reviewer": map[string]interface{}{
					"status":         "completed",
					"childSessionId": "child-review",
				},
			},
		},
	}

	state := workflowRuntimeStateFromMetadata(sess)
	if state == nil {
		t.Fatalf("expected workflow state")
	}
	if state.Nodes["developer"] == nil || state.Nodes["developer"].ChildSessionID != "child-dev" {
		t.Fatalf("expected developer child session to round-trip, got: %#v", state.Nodes["developer"])
	}
	if state.Nodes["reviewer"] == nil || state.Nodes["reviewer"].ChildSessionID != "child-review" {
		t.Fatalf("expected reviewer child session to round-trip, got: %#v", state.Nodes["reviewer"])
	}
}

func TestWorkflowDefinitionFromMetadataNormalizesStoredWorkflowMap(t *testing.T) {
	sess := session.New("build")
	sess.Metadata = map[string]interface{}{
		"workflow": map[string]interface{}{
			"id":          "wf-legacy",
			"name":        "Legacy workflow",
			"entryNodeId": " builder ",
			"policy": map[string]interface{}{
				"stopCondition":  "max_turns",
				"judgeNodeId":    " judge ",
				"maxTurns":       float64(7),
				"timeboxMinutes": float64(15),
			},
			"nodes": []interface{}{
				map[string]interface{}{"id": " user ", "kind": "USER", "label": "  Requester  "},
				map[string]interface{}{"id": " builder ", "label": "  ", "kind": " Main ", "ref": " build-agent "},
				map[string]interface{}{"id": " critic ", "label": " Critic ", "kind": " SubAgent "},
				map[string]interface{}{"id": "   ", "kind": "main"},
				"not-a-node",
			},
			"edges": []interface{}{
				map[string]interface{}{"from": " user ", "to": " builder ", "mode": " Sequential "},
				map[string]interface{}{"from": " builder ", "to": " critic ", "mode": " FAN_OUT "},
				map[string]interface{}{"from": "builder", "to": "   "},
				"not-an-edge",
			},
		},
	}

	def, ok := workflowDefinitionFromMetadata(sess)
	if !ok {
		t.Fatal("expected legacy workflow metadata to parse")
	}
	if def.ID != "wf-legacy" || def.Name != "Legacy workflow" {
		t.Fatalf("unexpected workflow identity: %+v", def)
	}
	if def.EntryNodeID != " builder " {
		t.Fatalf("expected raw entry node id to round-trip, got %q", def.EntryNodeID)
	}
	if def.Policy.StopCondition != "max_turns" || def.Policy.JudgeNodeID != " judge " || def.Policy.MaxTurns != 7 || def.Policy.TimeboxMins != 15 {
		t.Fatalf("unexpected workflow policy: %+v", def.Policy)
	}
	if len(def.Nodes) != 3 {
		t.Fatalf("expected malformed node entries to be skipped, got %d nodes", len(def.Nodes))
	}
	if def.Nodes[0].ID != "user" || def.Nodes[0].Label != "Requester" || def.Nodes[0].Kind != "user" {
		t.Fatalf("unexpected normalized user node: %+v", def.Nodes[0])
	}
	if def.Nodes[1].ID != "builder" || def.Nodes[1].Label != "builder" || def.Nodes[1].Kind != "main" || def.Nodes[1].Ref != "build-agent" {
		t.Fatalf("unexpected normalized builder node: %+v", def.Nodes[1])
	}
	if def.Nodes[2].ID != "critic" || def.Nodes[2].Label != "Critic" || def.Nodes[2].Kind != "subagent" {
		t.Fatalf("unexpected normalized critic node: %+v", def.Nodes[2])
	}
	if len(def.Edges) != 2 {
		t.Fatalf("expected malformed edges to be skipped, got %d edges", len(def.Edges))
	}
	if def.Edges[0] != (workflowEdgeRuntime{From: "user", To: "builder", Mode: "sequential"}) {
		t.Fatalf("unexpected normalized edge[0]: %+v", def.Edges[0])
	}
	if def.Edges[1] != (workflowEdgeRuntime{From: "builder", To: "critic", Mode: "fan_out"}) {
		t.Fatalf("unexpected normalized edge[1]: %+v", def.Edges[1])
	}
}

func TestWorkflowDefinitionFromMetadataExpandsReviewLoopAndRewritesEdges(t *testing.T) {
	sess := session.New("build")
	sess.Metadata = map[string]interface{}{
		workflowDefinitionMetadataKey: map[string]interface{}{
			"id":          "wf-review",
			"entryNodeId": "review",
			"nodes": []interface{}{
				map[string]interface{}{"id": "user", "kind": "user"},
				map[string]interface{}{
					"id":                  "review",
					"kind":                "review_loop",
					"instruction":         "default worker instruction",
					"workerSubAgentId":    "worker-sa",
					"workerLabel":         "Builder",
					"workerInstruction":   "implement the requested change",
					"reviewerSubAgentId":  "critic-sa",
					"reviewerLabel":       "Critic",
					"reviewerInstruction": "review carefully",
					"loopMaxTurns":        float64(4),
				},
				map[string]interface{}{"id": "reporter", "kind": "main", "label": "Reporter"},
			},
			"edges": []interface{}{
				map[string]interface{}{"from": "user", "to": "review", "mode": "sequential"},
				map[string]interface{}{"from": "review", "to": "reporter", "mode": "sequential"},
			},
		},
	}

	def, ok := workflowDefinitionFromMetadata(sess)
	if !ok {
		t.Fatal("expected review-loop workflow metadata to parse")
	}
	if def.EntryNodeID != "review__worker" {
		t.Fatalf("expected review loop entry node to rewrite to worker, got %q", def.EntryNodeID)
	}
	if def.Policy.StopCondition != "judge" || def.Policy.JudgeNodeID != "review__critic" || def.Policy.MaxTurns != 4 {
		t.Fatalf("unexpected rewritten review-loop policy: %+v", def.Policy)
	}

	nodesByID := make(map[string]workflowNodeRuntime, len(def.Nodes))
	for _, node := range def.Nodes {
		nodesByID[node.ID] = node
	}
	if _, exists := nodesByID["review"]; exists {
		t.Fatal("expected review_loop placeholder node to be replaced")
	}
	if worker := nodesByID["review__worker"]; worker.Kind != "subagent" || worker.SubAgentID != "worker-sa" || worker.Label != "Builder" || worker.Instruction != "implement the requested change" {
		t.Fatalf("unexpected worker node: %+v", worker)
	}
	if critic := nodesByID["review__critic"]; critic.Kind != "subagent" || critic.SubAgentID != "critic-sa" || critic.Label != "Critic" || !strings.Contains(critic.Instruction, "VERDICT: APPROVED") {
		t.Fatalf("unexpected critic node: %+v", critic)
	}

	edges := make(map[workflowEdgeRuntime]bool, len(def.Edges))
	for _, edge := range def.Edges {
		edges[edge] = true
		if edge.From == "review" || edge.To == "review" {
			t.Fatalf("expected rewritten edges to avoid original review node, got %+v", edge)
		}
	}
	for _, want := range []workflowEdgeRuntime{
		{From: "user", To: "review__worker", Mode: "sequential"},
		{From: "review__worker", To: "review__critic", Mode: "sequential"},
		{From: "review__critic", To: "review__worker", Mode: "sequential"},
		{From: "review__critic", To: "reporter", Mode: "sequential"},
	} {
		if !edges[want] {
			t.Fatalf("expected rewritten edge %+v, got %+v", want, def.Edges)
		}
	}
}
