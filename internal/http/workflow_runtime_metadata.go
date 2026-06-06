package http

import (
	"encoding/json"

	"strings"

	"github.com/A2gent/brute/internal/session"
)

func workflowTranscriptEntriesFromMetadata(raw interface{}) []workflowTranscriptEntry {
	if raw == nil {
		return nil
	}
	if entries, ok := raw.([]workflowTranscriptEntry); ok {
		return entries
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var entries []workflowTranscriptEntry
	if err := json.Unmarshal(bytes, &entries); err != nil {
		return nil
	}
	return entries
}

func workflowDefinitionFromMetadata(sess *session.Session) (*workflowDefinitionRuntime, bool) {
	if sess == nil || sess.Metadata == nil {
		return nil, false
	}
	raw, ok := sess.Metadata[workflowDefinitionMetadataKey]
	if !ok {
		raw, ok = sess.Metadata["workflow"]
		if !ok {
			return nil, false
		}
	}
	root, ok := raw.(map[string]interface{})
	if !ok {
		return nil, false
	}
	nodesRaw, ok := root["nodes"].([]interface{})
	if !ok || len(nodesRaw) == 0 {
		return nil, false
	}

	def := &workflowDefinitionRuntime{
		ID:          asWorkflowString(root["id"]),
		Name:        asWorkflowString(root["name"]),
		Description: asWorkflowString(root["description"]),
		EntryNodeID: asWorkflowString(root["entryNodeId"]),
		Nodes:       make([]workflowNodeRuntime, 0, len(nodesRaw)),
	}
	if policyRaw, ok := root["policy"].(map[string]interface{}); ok {
		def.Policy = workflowPolicyRuntime{
			StopCondition: asWorkflowString(policyRaw["stopCondition"]),
			JudgeNodeID:   asWorkflowString(policyRaw["judgeNodeId"]),
			MaxTurns:      asWorkflowInt(policyRaw["maxTurns"]),
			TimeboxMins:   asWorkflowInt(policyRaw["timeboxMinutes"]),
		}
	}
	for _, item := range nodesRaw {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id := strings.TrimSpace(asWorkflowString(row["id"]))
		if id == "" {
			continue
		}
		label := strings.TrimSpace(asWorkflowString(row["label"]))
		if label == "" {
			label = id
		}
		kind := strings.TrimSpace(strings.ToLower(asWorkflowString(row["kind"])))
		if kind == "" {
			kind = "main"
		}
		def.Nodes = append(def.Nodes, workflowNodeRuntime{
			ID:                  id,
			Label:               label,
			Kind:                kind,
			Ref:                 strings.TrimSpace(asWorkflowString(row["ref"])),
			SubAgentID:          strings.TrimSpace(asWorkflowString(row["subAgentId"])),
			LocalAgentID:        strings.TrimSpace(asWorkflowString(row["localAgentId"])),
			ExternalAgentID:     strings.TrimSpace(asWorkflowString(row["externalAgentId"])),
			Instruction:         strings.TrimSpace(asWorkflowString(row["instruction"])),
			WorkerSubAgentID:    strings.TrimSpace(asWorkflowString(row["workerSubAgentId"])),
			WorkerLabel:         strings.TrimSpace(asWorkflowString(row["workerLabel"])),
			WorkerInstruction:   strings.TrimSpace(asWorkflowString(row["workerInstruction"])),
			ReviewerSubAgentID:  strings.TrimSpace(asWorkflowString(row["reviewerSubAgentId"])),
			ReviewerLabel:       strings.TrimSpace(asWorkflowString(row["reviewerLabel"])),
			ReviewerInstruction: strings.TrimSpace(asWorkflowString(row["reviewerInstruction"])),
			LoopMaxTurns:        asWorkflowInt(row["loopMaxTurns"]),
		})
	}
	if edgesRaw, ok := root["edges"].([]interface{}); ok {
		def.Edges = make([]workflowEdgeRuntime, 0, len(edgesRaw))
		for _, item := range edgesRaw {
			row, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			from := strings.TrimSpace(asWorkflowString(row["from"]))
			to := strings.TrimSpace(asWorkflowString(row["to"]))
			if from == "" || to == "" {
				continue
			}
			def.Edges = append(def.Edges, workflowEdgeRuntime{
				From: from,
				To:   to,
				Mode: strings.TrimSpace(strings.ToLower(asWorkflowString(row["mode"]))),
			})
		}
	}
	if len(def.Nodes) == 0 {
		return nil, false
	}
	expandReviewLoopNodes(def)
	return def, true
}

func workflowRuntimeStateFromMetadata(sess *session.Session) *workflowRuntimeState {
	if sess == nil || sess.Metadata == nil {
		return nil
	}
	raw, ok := sess.Metadata[workflowStateMetadataKey]
	if !ok || raw == nil {
		return nil
	}
	if state, ok := raw.(*workflowRuntimeState); ok {
		return state
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var state workflowRuntimeState
	if err := json.Unmarshal(bytes, &state); err != nil {
		return nil
	}
	return &state
}

func expandReviewLoopNodes(def *workflowDefinitionRuntime) {
	if def == nil {
		return
	}
	nextNodes := make([]workflowNodeRuntime, 0, len(def.Nodes))
	nextEdges := make([]workflowEdgeRuntime, 0, len(def.Edges))
	loopByID := map[string]workflowNodeRuntime{}
	for _, node := range def.Nodes {
		if strings.EqualFold(strings.TrimSpace(node.Kind), "review_loop") {
			loopByID[node.ID] = node
			continue
		}
		nextNodes = append(nextNodes, node)
	}
	if len(loopByID) == 0 {
		return
	}
	for loopID, loop := range loopByID {
		workerID := loop.ID + "__worker"
		reviewerID := loop.ID + "__critic"
		workerLabel := strings.TrimSpace(loop.WorkerLabel)
		if workerLabel == "" {
			workerLabel = "Worker"
		}
		reviewerLabel := strings.TrimSpace(loop.ReviewerLabel)
		if reviewerLabel == "" {
			reviewerLabel = "Critic"
		}
		nextNodes = append(nextNodes,
			workflowNodeRuntime{
				ID:          workerID,
				Label:       workerLabel,
				Kind:        "subagent",
				SubAgentID:  strings.TrimSpace(loop.WorkerSubAgentID),
				Instruction: workflowReviewLoopWorkerInstruction(loop),
			},
			workflowNodeRuntime{
				ID:          reviewerID,
				Label:       reviewerLabel,
				Kind:        "subagent",
				SubAgentID:  strings.TrimSpace(loop.ReviewerSubAgentID),
				Instruction: workflowReviewLoopReviewerInstruction(loop),
			},
		)
		nextEdges = append(nextEdges,
			workflowEdgeRuntime{From: workerID, To: reviewerID, Mode: "sequential"},
			workflowEdgeRuntime{From: reviewerID, To: workerID, Mode: "sequential"},
		)
		if def.EntryNodeID == loopID {
			def.EntryNodeID = workerID
		}
		if strings.TrimSpace(def.Policy.JudgeNodeID) == "" || strings.TrimSpace(def.Policy.JudgeNodeID) == loopID {
			def.Policy.JudgeNodeID = reviewerID
		}
		def.Policy.StopCondition = "judge"
		if loop.LoopMaxTurns > 0 {
			def.Policy.MaxTurns = loop.LoopMaxTurns
		}
	}
	for _, edge := range def.Edges {
		from := strings.TrimSpace(edge.From)
		to := strings.TrimSpace(edge.To)
		if loop, ok := loopByID[from]; ok {
			from = loop.ID + "__critic"
		}
		if loop, ok := loopByID[to]; ok {
			to = loop.ID + "__worker"
		}
		if from == "" || to == "" {
			continue
		}
		if _, fromWasLoop := loopByID[edge.From]; fromWasLoop {
			if _, toWasLoop := loopByID[edge.To]; toWasLoop {
				continue
			}
		}
		nextEdges = append(nextEdges, workflowEdgeRuntime{From: from, To: to, Mode: edge.Mode})
	}
	def.Nodes = nextNodes
	def.Edges = nextEdges
}

func workflowChildContextSeeded(child *session.Session) bool {
	if child == nil || child.Metadata == nil {
		return false
	}
	value, ok := child.Metadata[workflowContextSeededKey]
	if !ok {
		return false
	}
	if seeded, ok := value.(bool); ok {
		return seeded
	}
	if seeded, ok := value.(string); ok {
		return strings.EqualFold(strings.TrimSpace(seeded), "true")
	}
	return false
}

func workflowSessionStatus(sess *session.Session) session.Status {
	if sess == nil || sess.Metadata == nil {
		return session.StatusCompleted
	}
	raw, ok := sess.Metadata[workflowStateMetadataKey]
	if !ok {
		return session.StatusCompleted
	}
	state := &workflowRuntimeState{}
	if b, err := json.Marshal(raw); err == nil {
		_ = json.Unmarshal(b, state)
	}
	switch strings.ToLower(strings.TrimSpace(state.Status)) {
	case "failed":
		return session.StatusFailed
	case "blocked", "in_progress", "running":
		return session.StatusPaused
	default:
		return session.StatusCompleted
	}
}

func asWorkflowString(raw interface{}) string {
	if v, ok := raw.(string); ok {
		return v
	}
	return ""
}

func asWorkflowInt(raw interface{}) int {
	switch v := raw.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
