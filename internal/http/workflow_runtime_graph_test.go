package http

import (
	"strings"
	"testing"
)

func TestNewWorkflowGraphIgnoresMalformedEdges(t *testing.T) {
	def := &workflowDefinitionRuntime{
		Nodes: []workflowNodeRuntime{
			{ID: "user", Kind: "user"},
			{ID: "builder", Kind: "main"},
			{ID: "critic", Kind: "subagent"},
		},
		Edges: []workflowEdgeRuntime{
			{From: "user", To: "builder"},
			{From: "builder", To: "critic"},
			{From: "missing", To: "critic"},
			{From: "builder", To: "missing"},
			{From: "", To: "critic"},
		},
	}

	graph := newWorkflowGraph(def)

	if graph.HasCycle {
		t.Fatal("did not expect an acyclic graph to report a cycle")
	}
	if got := graph.Preds["critic"]; len(got) != 1 || got[0] != "builder" {
		t.Fatalf("expected only valid builder predecessor for critic, got %#v", got)
	}
	if got := graph.Succ["builder"]; len(got) != 1 || got[0] != "critic" {
		t.Fatalf("expected only valid critic successor for builder, got %#v", got)
	}
}

func TestNewWorkflowGraphDetectsSelfCycle(t *testing.T) {
	def := &workflowDefinitionRuntime{
		Nodes: []workflowNodeRuntime{{ID: "worker", Kind: "main"}},
		Edges: []workflowEdgeRuntime{{From: "worker", To: "worker"}},
	}

	graph := newWorkflowGraph(def)

	if !graph.HasCycle {
		t.Fatal("expected self-edge to be detected as a cycle")
	}
}

func TestWorkflowReadyNodesWaitsForCompleteUpstream(t *testing.T) {
	nodes := []workflowNodeRuntime{
		{ID: "user", Kind: "user"},
		{ID: "builder", Kind: "main"},
		{ID: "critic", Kind: "subagent"},
	}
	preds := map[string][]string{
		"builder": {"user"},
		"critic":  {"builder"},
	}
	succ := map[string][]string{
		"user":    {"builder"},
		"builder": {"critic"},
	}
	sccByNode, _ := workflowSCC(nodes, succ)
	actionable := map[string]workflowNodeRuntime{
		"builder": nodes[1],
		"critic":  nodes[2],
	}
	turnState := map[string]*workflowTurnNodeState{
		"builder": {LastConsumedByDep: map[string]int{}},
		"critic":  {LastConsumedByDep: map[string]int{}},
	}

	ready := workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, nil, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "builder" {
		t.Fatalf("expected builder to be ready first, got %#v", ready)
	}

	turnState["builder"].RunCount = 1
	turnState["builder"].LastConsumedByDep["user"] = 1
	ready = workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, nil, turnState, sccByNode)
	if len(ready) != 0 {
		t.Fatalf("expected critic to wait while builder has no complete output, got %#v", ready)
	}

	ready = workflowReadyNodes(actionable, preds, map[string]int{"user": 1, "builder": 1}, nil, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "critic" {
		t.Fatalf("expected critic after builder complete output, got %#v", ready)
	}
}

func TestWorkflowReadyNodesRetriesInProgressNode(t *testing.T) {
	nodes := []workflowNodeRuntime{
		{ID: "user", Kind: "user"},
		{ID: "builder", Kind: "main"},
	}
	preds := map[string][]string{
		"builder": {"user"},
	}
	succ := map[string][]string{
		"user": {"builder"},
	}
	sccByNode, _ := workflowSCC(nodes, succ)
	actionable := map[string]workflowNodeRuntime{
		"builder": nodes[1],
	}
	turnState := map[string]*workflowTurnNodeState{
		"builder": {
			RunCount:          1,
			LastConsumedByDep: map[string]int{"user": 1},
		},
	}

	ready := workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, map[string]bool{"builder": true}, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "builder" {
		t.Fatalf("expected in-progress builder to be retried, got %#v", ready)
	}
}

func TestWorkflowNodesBlockedByNeverRunDepsIgnoresCycleInternalDeps(t *testing.T) {
	unreached := []string{"worker", "critic", "reporter"}
	preds := map[string][]string{
		"worker":   {"critic"},
		"critic":   {"worker"},
		"reporter": {"never-ran"},
	}
	sccByNode := map[string]int{
		"worker":    1,
		"critic":    1,
		"reporter":  2,
		"never-ran": 3,
	}

	blocked := workflowNodesBlockedByNeverRunDeps(unreached, preds, map[string]int{"worker": 0, "critic": 0, "reporter": 0}, sccByNode)

	if len(blocked) != 1 || blocked[0] != "reporter" {
		t.Fatalf("expected only reporter to be blocked by an external never-run dep, got %#v", blocked)
	}
	diagnostic := workflowPendingDependencyDiagnostic(blocked, preds, map[string]int{}, sccByNode)
	if !strings.Contains(diagnostic, "reporter<-never-ran") {
		t.Fatalf("expected diagnostic to name missing dependency, got %q", diagnostic)
	}
}

func TestWorkflowReadyNodesAllowsCycleNodeWithExternalSeedInput(t *testing.T) {
	nodes := []workflowNodeRuntime{
		{ID: "user", Kind: "user"},
		{ID: "worker", Kind: "subagent"},
		{ID: "critic", Kind: "subagent"},
	}
	preds := map[string][]string{
		"worker": {"user", "critic"},
		"critic": {"worker"},
	}
	succ := map[string][]string{
		"user":   {"worker"},
		"worker": {"critic"},
		"critic": {"worker"},
	}
	sccByNode, _ := workflowSCC(nodes, succ)
	actionable := map[string]workflowNodeRuntime{
		"worker": nodes[1],
		"critic": nodes[2],
	}
	turnState := map[string]*workflowTurnNodeState{
		"worker": {LastConsumedByDep: map[string]int{}},
		"critic": {LastConsumedByDep: map[string]int{}},
	}

	ready := workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, nil, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "worker" {
		t.Fatalf("expected worker to start from external seed input, got %#v", ready)
	}

	turnState["worker"].RunCount = 1
	turnState["worker"].LastConsumedByDep["user"] = 1
	ready = workflowReadyNodes(actionable, preds, map[string]int{"user": 1, "worker": 1}, nil, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "critic" {
		t.Fatalf("expected critic after first worker handoff, got %#v", ready)
	}

	turnState["critic"].RunCount = 1
	turnState["critic"].LastConsumedByDep["worker"] = 1
	ready = workflowReadyNodes(actionable, preds, map[string]int{"user": 1, "worker": 1, "critic": 1}, nil, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "worker" {
		t.Fatalf("expected worker to resume after critic feedback, got %#v", ready)
	}
}
