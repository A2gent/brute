package http

import (
	"sort"
	"strings"
)

func newWorkflowGraph(def *workflowDefinitionRuntime) workflowGraph {
	graph := workflowGraph{
		NodeByID: make(map[string]workflowNodeRuntime),
		Preds:    make(map[string][]string),
		Succ:     make(map[string][]string),
	}
	if def == nil {
		graph.SCCByNode, graph.SCCSize = workflowSCC(nil, graph.Succ)
		return graph
	}
	for _, node := range def.Nodes {
		nodeID := strings.TrimSpace(node.ID)
		if nodeID == "" {
			continue
		}
		node.ID = nodeID
		graph.NodeByID[nodeID] = node
	}
	for _, edge := range def.Edges {
		from := strings.TrimSpace(edge.From)
		to := strings.TrimSpace(edge.To)
		if from == "" || to == "" {
			continue
		}
		if _, ok := graph.NodeByID[from]; !ok {
			continue
		}
		if _, ok := graph.NodeByID[to]; !ok {
			continue
		}
		graph.Preds[to] = append(graph.Preds[to], from)
		graph.Succ[from] = append(graph.Succ[from], to)
	}
	graph.SCCByNode, graph.SCCSize = workflowSCC(def.Nodes, graph.Succ)
	graph.HasCycle = workflowHasCycle(def.Nodes, graph.Succ, graph.SCCByNode, graph.SCCSize)
	return graph
}

func workflowSCC(nodes []workflowNodeRuntime, succ map[string][]string) (map[string]int, map[int]int) {
	index := 0
	stack := make([]string, 0, len(nodes))
	onStack := make(map[string]bool, len(nodes))
	indexByNode := make(map[string]int, len(nodes))
	lowLink := make(map[string]int, len(nodes))
	sccByNode := make(map[string]int, len(nodes))
	sccSize := map[int]int{}

	var strongConnect func(nodeID string)
	strongConnect = func(nodeID string) {
		indexByNode[nodeID] = index
		lowLink[nodeID] = index
		index++
		stack = append(stack, nodeID)
		onStack[nodeID] = true

		for _, nextID := range succ[nodeID] {
			if _, seen := indexByNode[nextID]; !seen {
				strongConnect(nextID)
				if lowLink[nextID] < lowLink[nodeID] {
					lowLink[nodeID] = lowLink[nextID]
				}
			} else if onStack[nextID] && indexByNode[nextID] < lowLink[nodeID] {
				lowLink[nodeID] = indexByNode[nextID]
			}
		}

		if lowLink[nodeID] == indexByNode[nodeID] {
			sccID := len(sccSize)
			for {
				last := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[last] = false
				sccByNode[last] = sccID
				sccSize[sccID]++
				if last == nodeID {
					break
				}
			}
		}
	}

	for _, node := range nodes {
		if _, seen := indexByNode[node.ID]; seen {
			continue
		}
		strongConnect(node.ID)
	}
	return sccByNode, sccSize
}

func workflowHasCycle(
	nodes []workflowNodeRuntime,
	succ map[string][]string,
	sccByNode map[string]int,
	sccSize map[int]int,
) bool {
	for _, size := range sccSize {
		if size > 1 {
			return true
		}
	}
	for _, node := range nodes {
		for _, nextID := range succ[node.ID] {
			if nextID == node.ID && sccByNode[nextID] == sccByNode[node.ID] {
				return true
			}
		}
	}
	return false
}

func workflowReadyNodes(
	actionable map[string]workflowNodeRuntime,
	preds map[string][]string,
	completedVersion map[string]int,
	retryRequested map[string]bool,
	nodeTurnState map[string]*workflowTurnNodeState,
	sccByNode map[string]int,
) []workflowNodeRuntime {
	ready := make([]workflowNodeRuntime, 0, len(actionable))
	for nodeID, node := range actionable {
		if retryRequested[nodeID] {
			ready = append(ready, node)
			continue
		}
		ts := nodeTurnState[nodeID]
		if ts == nil {
			continue
		}
		readyForRun := true
		hasInput := len(preds[nodeID]) == 0
		hasNewInput := false

		for _, dep := range preds[nodeID] {
			depVersion := completedVersion[dep]
			if depVersion > 0 {
				hasInput = true
			}
			if sccByNode[dep] != sccByNode[nodeID] && depVersion == 0 {
				readyForRun = false
				break
			}
			lastConsumed := ts.LastConsumedByDep[dep]
			if depVersion > lastConsumed {
				hasNewInput = true
			}
		}
		if !readyForRun {
			continue
		}
		if ts.RunCount == 0 {
			if hasInput {
				ready = append(ready, node)
			}
			continue
		}
		if hasNewInput {
			ready = append(ready, node)
		}
	}
	return ready
}

func workflowUnreachedActionableNodes(actionable map[string]workflowNodeRuntime, runVersion map[string]int) []string {
	ids := make([]string, 0, len(actionable))
	for nodeID := range actionable {
		if runVersion[nodeID] == 0 {
			ids = append(ids, nodeID)
		}
	}
	sort.Strings(ids)
	return ids
}

func workflowNodesBlockedByNeverRunDeps(
	unreached []string,
	preds map[string][]string,
	runVersion map[string]int,
	sccByNode map[string]int,
) []string {
	blocked := make([]string, 0, len(unreached))
	for _, nodeID := range unreached {
		for _, dep := range preds[nodeID] {
			if sccByNode[dep] == sccByNode[nodeID] {
				continue
			}
			if runVersion[dep] == 0 {
				blocked = append(blocked, nodeID)
				break
			}
		}
	}
	sort.Strings(blocked)
	return blocked
}

func workflowPendingDependencyDiagnostic(
	unreached []string,
	preds map[string][]string,
	runVersion map[string]int,
	sccByNode map[string]int,
) string {
	if len(unreached) == 0 {
		return "workflow graph stalled: no runnable nodes remain"
	}
	details := make([]string, 0, len(unreached))
	for _, nodeID := range unreached {
		missingExternal := make([]string, 0)
		for _, dep := range preds[nodeID] {
			if sccByNode[dep] == sccByNode[nodeID] {
				continue
			}
			if runVersion[dep] == 0 {
				missingExternal = append(missingExternal, dep)
			}
		}
		if len(missingExternal) == 0 {
			details = append(details, nodeID+"<-none")
			continue
		}
		sort.Strings(missingExternal)
		details = append(details, nodeID+"<-"+strings.Join(missingExternal, "|"))
	}
	return "workflow graph stalled: no runnable nodes remain; blocked external dependencies: " + strings.Join(details, "; ")
}
