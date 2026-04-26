package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

const (
	workflowDefinitionMetadataKey = "workflow_definition"
	workflowStateMetadataKey      = "workflow_state"
)

type workflowDefinitionRuntime struct {
	ID          string
	Name        string
	Description string
	EntryNodeID string
	Nodes       []workflowNodeRuntime
	Edges       []workflowEdgeRuntime
	Policy      workflowPolicyRuntime
}

type workflowNodeRuntime struct {
	ID                 string
	Label              string
	Kind               string
	Ref                string
	SubAgentID         string
	LocalAgentID       string
	ExternalAgentID    string
	Instruction        string
	WorkerSubAgentID   string
	WorkerLabel        string
	ReviewerSubAgentID string
	ReviewerLabel      string
	LoopMaxTurns       int
}

type workflowEdgeRuntime struct {
	From string
	To   string
	Mode string
}

type workflowPolicyRuntime struct {
	StopCondition string
	JudgeNodeID   string
	MaxTurns      int
	TimeboxMins   int
}

type workflowRuntimeNodeState struct {
	Status         string `json:"status"`
	ChildSessionID string `json:"childSessionId,omitempty"`
	StartedAt      string `json:"startedAt,omitempty"`
	CompletedAt    string `json:"completedAt,omitempty"`
	Error          string `json:"error,omitempty"`
	OutputPreview  string `json:"outputPreview,omitempty"`
}

type workflowRuntimeState struct {
	WorkflowID   string                               `json:"workflowId,omitempty"`
	WorkflowName string                               `json:"workflowName,omitempty"`
	Status       string                               `json:"status"`
	UpdatedAt    string                               `json:"updatedAt"`
	Nodes        map[string]*workflowRuntimeNodeState `json:"nodes"`
}

type workflowNodeResult struct {
	nodeID         string
	nodeLabel      string
	childSessionID string
	output         string
	workStatus     string
	err            error
}

type workflowTurnNodeState struct {
	RunCount          int
	LastConsumedByDep map[string]int
}

func (s *Server) hasRunnableWorkflow(sess *session.Session) bool {
	def, ok := workflowDefinitionFromMetadata(sess)
	if !ok {
		return false
	}
	if isSimpleUserMainWorkflow(def) {
		return false
	}
	actionable := 0
	for _, node := range def.Nodes {
		if strings.ToLower(strings.TrimSpace(node.Kind)) != "user" {
			actionable++
		}
	}
	return actionable > 0
}

func isSimpleUserMainWorkflow(def *workflowDefinitionRuntime) bool {
	if def == nil || len(def.Nodes) == 0 {
		return false
	}
	var userCount int
	var mainCount int
	for _, node := range def.Nodes {
		kind := strings.ToLower(strings.TrimSpace(node.Kind))
		switch kind {
		case "user":
			userCount++
		case "main":
			mainCount++
		default:
			return false
		}
	}
	if userCount != 1 || mainCount != 1 || len(def.Nodes) != 2 {
		return false
	}
	return true
}

func (s *Server) runWorkflowSession(
	ctx context.Context,
	sess *session.Session,
	userMessage string,
	emit func(ChatStreamEvent) bool,
) (string, llm.TokenUsage, error) {
	def, ok := workflowDefinitionFromMetadata(sess)
	if !ok {
		return "", llm.TokenUsage{}, fmt.Errorf("workflow metadata is missing")
	}
	nodeByID := make(map[string]workflowNodeRuntime, len(def.Nodes))
	preds := make(map[string][]string)
	succ := make(map[string][]string)
	for _, node := range def.Nodes {
		nodeByID[node.ID] = node
	}
	for _, edge := range def.Edges {
		from := strings.TrimSpace(edge.From)
		to := strings.TrimSpace(edge.To)
		if from == "" || to == "" {
			continue
		}
		if _, ok := nodeByID[from]; !ok {
			continue
		}
		if _, ok := nodeByID[to]; !ok {
			continue
		}
		preds[to] = append(preds[to], from)
		succ[from] = append(succ[from], to)
	}
	sccByNode, sccSize := workflowSCC(def.Nodes, succ)
	hasCycle := workflowHasCycle(def.Nodes, succ, sccByNode, sccSize)

	state := &workflowRuntimeState{
		WorkflowID:   def.ID,
		WorkflowName: def.Name,
		Status:       "running",
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		Nodes:        make(map[string]*workflowRuntimeNodeState, len(def.Nodes)),
	}
	for _, node := range def.Nodes {
		st := &workflowRuntimeNodeState{Status: "pending"}
		if strings.EqualFold(node.Kind, "user") {
			st.Status = "completed"
			st.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			st.OutputPreview = preview(userMessage, 220)
		}
		state.Nodes[node.ID] = st
	}
	if err := s.persistWorkflowState(sess, state, emit); err != nil {
		return "", llm.TokenUsage{}, err
	}

	outputs := map[string]string{}
	runVersion := map[string]int{}
	completeVersion := map[string]int{}
	retryRequested := map[string]bool{}
	nodeTurnState := map[string]*workflowTurnNodeState{}
	actionable := map[string]workflowNodeRuntime{}
	for _, node := range def.Nodes {
		if strings.EqualFold(node.Kind, "user") {
			outputs[node.ID] = userMessage
			runVersion[node.ID] = 1
			completeVersion[node.ID] = 1
			continue
		}
		actionable[node.ID] = node
		nodeTurnState[node.ID] = &workflowTurnNodeState{
			LastConsumedByDep: make(map[string]int),
		}
	}

	maxTurns := workflowMaxTurns(def)
	turnsUsed := 0
	deadline := time.Now().Add(time.Duration(workflowTimeboxMinutes(def)) * time.Minute)
	judgeID := strings.TrimSpace(def.Policy.JudgeNodeID)
	stopCondition := strings.ToLower(strings.TrimSpace(def.Policy.StopCondition))
	enforceTurnCap := hasCycle || stopCondition == "max_turns"
	exitReason := "no_ready"
	for len(actionable) > 0 {
		if time.Now().After(deadline) {
			exitReason = "timebox"
			break
		}
		if (enforceTurnCap || len(retryRequested) > 0) && turnsUsed >= maxTurns {
			exitReason = "turn_cap"
			break
		}
		ready := workflowReadyNodes(actionable, preds, completeVersion, retryRequested, nodeTurnState, sccByNode)
		if len(ready) == 0 {
			exitReason = "no_ready"
			break
		}
		turnsUsed++
		sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
		results := make(chan workflowNodeResult, len(ready))
		var wg sync.WaitGroup
		for _, node := range ready {
			node := node
			ts := nodeTurnState[node.ID]
			wasRetry := retryRequested[node.ID]
			delete(retryRequested, node.ID)
			st := state.Nodes[node.ID]
			if st != nil {
				st.Status = "running"
				st.StartedAt = time.Now().UTC().Format(time.RFC3339)
				st.Error = ""
			}
			upstream := make([]string, 0, len(preds[node.ID]))
			for _, dep := range preds[node.ID] {
				if version := completeVersion[dep]; version > 0 && ts != nil {
					ts.LastConsumedByDep[dep] = version
				}
				if output := strings.TrimSpace(outputs[dep]); output != "" {
					upstream = append(upstream, output)
				}
			}
			previousNodeOutput := ""
			if wasRetry {
				previousNodeOutput = strings.TrimSpace(outputs[node.ID])
			}
			child, childErr := s.workflowNodeChildSession(sess, def, node, st)
			if childErr != nil {
				if st == nil {
					st = &workflowRuntimeNodeState{}
					state.Nodes[node.ID] = st
				}
				st.Status = "failed"
				st.Error = childErr.Error()
				st.CompletedAt = time.Now().UTC().Format(time.RFC3339)
				state.Status = "failed"
				_ = s.persistWorkflowState(sess, state, emit)
				return "", llm.TokenUsage{}, fmt.Errorf("node %q failed: %w", node.Label, childErr)
			}
			if st != nil {
				st.ChildSessionID = child.ID
			}
			if err := s.persistWorkflowState(sess, state, emit); err != nil {
				return "", llm.TokenUsage{}, err
			}
			wg.Add(1)
			go func(child *session.Session, upstream []string, previousNodeOutput string) {
				defer wg.Done()
				output, childSessionID, err := s.executeWorkflowNode(ctx, sess, def, node, userMessage, upstream, previousNodeOutput, child)
				results <- workflowNodeResult{
					nodeID:         node.ID,
					nodeLabel:      node.Label,
					childSessionID: childSessionID,
					output:         output,
					workStatus:     workflowNodeWorkStatusForSession(node, output, child, userMessage),
					err:            err,
				}
			}(child, upstream, previousNodeOutput)
		}
		wg.Wait()
		close(results)

		for result := range results {
			st := state.Nodes[result.nodeID]
			if st == nil {
				st = &workflowRuntimeNodeState{}
				state.Nodes[result.nodeID] = st
			}
			st.ChildSessionID = result.childSessionID
			if result.err != nil {
				st.Status = "failed"
				st.Error = result.err.Error()
				st.CompletedAt = time.Now().UTC().Format(time.RFC3339)
				state.Status = "failed"
				_ = s.persistWorkflowState(sess, state, emit)
				return "", llm.TokenUsage{}, fmt.Errorf("node %q failed: %w", result.nodeLabel, result.err)
			}
			switch result.workStatus {
			case "in_progress":
				st.Status = "in_progress"
				retryRequested[result.nodeID] = true
			case "blocked":
				st.Status = "blocked"
			default:
				st.Status = "completed"
			}
			st.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			st.OutputPreview = preview(result.output, 220)
			outputs[result.nodeID] = result.output
			runVersion[result.nodeID]++
			if result.workStatus == "complete" {
				completeVersion[result.nodeID]++
			}
			if ts := nodeTurnState[result.nodeID]; ts != nil {
				ts.RunCount++
			}
		}
		if err := s.persistWorkflowState(sess, state, emit); err != nil {
			return "", llm.TokenUsage{}, err
		}
		if stopCondition == "judge" && judgeID != "" {
			if workflowJudgeApproved(outputs[judgeID]) {
				exitReason = "judge_approved"
				break
			}
		}
	}

	unreachable := workflowUnreachedActionableNodes(actionable, runVersion)
	blockedByNeverRunDeps := workflowNodesBlockedByNeverRunDeps(unreachable, preds, runVersion, sccByNode)
	if exitReason == "no_ready" && len(blockedByNeverRunDeps) > 0 {
		diagnostic := workflowPendingDependencyDiagnostic(blockedByNeverRunDeps, preds, runVersion, sccByNode)
		now := time.Now().UTC().Format(time.RFC3339)
		for _, nodeID := range blockedByNeverRunDeps {
			st := state.Nodes[nodeID]
			if st == nil {
				st = &workflowRuntimeNodeState{}
				state.Nodes[nodeID] = st
			}
			st.Status = "failed"
			st.CompletedAt = now
			st.Error = diagnostic
		}
		state.Status = "failed"
		_ = s.persistWorkflowState(sess, state, emit)
		return "", llm.TokenUsage{}, errors.New(diagnostic)
	}

	final := workflowFinalOutput(def, outputs, succ)
	if stopCondition == "judge" && judgeID != "" && !workflowJudgeApproved(outputs[judgeID]) {
		if workflowStateHasBlockedOrInProgressNode(state) {
			state.Status = "blocked"
		} else {
			state.Status = "failed"
		}
	} else if workflowStateHasBlockedOrInProgressNode(state) {
		state.Status = "blocked"
	} else {
		state.Status = "completed"
	}
	if err := s.persistWorkflowState(sess, state, emit); err != nil {
		return "", llm.TokenUsage{}, err
	}
	return final, llm.TokenUsage{}, nil
}

func (s *Server) persistWorkflowState(sess *session.Session, state *workflowRuntimeState, emit func(ChatStreamEvent) bool) error {
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	sess.Metadata[workflowStateMetadataKey] = state
	if err := s.sessionManager.Save(sess); err != nil {
		return err
	}
	if emit != nil {
		_ = emit(ChatStreamEvent{Type: "workflow_update", Workflow: state})
	}
	return nil
}

func (s *Server) executeWorkflowNode(
	ctx context.Context,
	parent *session.Session,
	def *workflowDefinitionRuntime,
	node workflowNodeRuntime,
	userMessage string,
	upstreamOutputs []string,
	previousNodeOutput string,
	child *session.Session,
) (string, string, error) {
	if child == nil {
		return "", "", fmt.Errorf("workflow child session is nil")
	}

	nodePrompt := composeWorkflowNodePrompt(parent, def, node, userMessage, upstreamOutputs, previousNodeOutput)
	child.AddUserMessageWithImagesAndMetadata(nodePrompt, nil, workflowNodePromptMessageMetadata(parent, def, node))
	child.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(child); err != nil {
		return "", child.ID, fmt.Errorf("failed to save child prompt: %w", err)
	}

	providerType := s.resolveSessionProviderType(child)
	model := s.resolveSessionModel(child, providerType)
	routingPrompt := messageForRouting(nodePrompt, 0)
	target, err := s.resolveExecutionTarget(ctx, providerType, model, routingPrompt, child)
	if err != nil {
		child.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
		child.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(child)
		return "", child.ID, fmt.Errorf("provider resolution failed: %w", err)
	}
	if setSessionRoutedProviderAndModel(child, providerType, target.ProviderType, target.Model) {
		_ = s.sessionManager.Save(child)
	}

	agentConfig := agent.Config{
		Name:          child.AgentID,
		Model:         target.Model,
		SystemPrompt:  s.buildSystemPromptForWorkflowNode(child, node),
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}
	ag := agent.New(agentConfig, target.Client, s.toolManagerForSession(child), s.sessionManager)
	content, _, runErr := ag.RunWithEvents(ctx, child, nodePrompt, func(ev agent.Event) {
		if ev.Type == agent.EventProviderTrace && ev.Provider != nil {
			s.applyProviderTraceToSession(child, target.ProviderType, ev.Provider)
		}
	})
	if runErr != nil {
		adaptedErr := s.adaptProviderErrorMessage(target.ProviderType, runErr)
		child.AddAssistantMessage(fmt.Sprintf("Request failed: %s", adaptedErr.Error()), nil)
		child.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(child)
		return "", child.ID, adaptedErr
	}
	if child.Status != session.StatusCompleted {
		child.SetStatus(session.StatusCompleted)
		_ = s.sessionManager.Save(child)
	}
	return strings.TrimSpace(content), child.ID, nil
}

func workflowNodePromptMessageMetadata(parent *session.Session, def *workflowDefinitionRuntime, node workflowNodeRuntime) map[string]interface{} {
	metadata := map[string]interface{}{
		"internal_handoff":     true,
		"handoff_kind":         "workflow_node",
		"workflow_node_id":     node.ID,
		"workflow_node_label":  node.Label,
		"workflow_node_kind":   node.Kind,
		"workflow_parent_role": "workflow",
	}
	if parent != nil {
		metadata["workflow_parent_id"] = parent.ID
	}
	if def != nil {
		metadata["workflow_id"] = def.ID
		metadata["workflow_name"] = def.Name
	}
	return metadata
}

func (s *Server) workflowNodeChildSession(
	parent *session.Session,
	def *workflowDefinitionRuntime,
	node workflowNodeRuntime,
	st *workflowRuntimeNodeState,
) (*session.Session, error) {
	if st != nil {
		childSessionID := strings.TrimSpace(st.ChildSessionID)
		if childSessionID != "" {
			child, err := s.sessionManager.Get(childSessionID)
			if err == nil && child != nil {
				return child, nil
			}
			logging.Warn("Workflow node %s child session %s could not be loaded; creating replacement: %v", node.ID, childSessionID, err)
		}
	}
	return s.createWorkflowNodeChildSession(parent, def, node)
}

func (s *Server) buildSystemPromptForWorkflowNode(child *session.Session, node workflowNodeRuntime) string {
	if strings.EqualFold(strings.TrimSpace(node.Kind), "subagent") {
		if sa, err := s.resolveWorkflowSubAgent(node); err == nil && sa != nil {
			if snapshot := s.composeSubAgentSystemPromptSnapshot(sa, child); snapshot != nil && strings.TrimSpace(snapshot.CombinedPrompt) != "" {
				attachSessionSystemPromptSnapshot(child, snapshot)
				if saveErr := s.sessionManager.Save(child); saveErr != nil {
					return strings.TrimSpace(snapshot.CombinedPrompt)
				}
				return strings.TrimSpace(snapshot.CombinedPrompt)
			}
		}
	}
	return s.buildSystemPromptForSession(child)
}

func (s *Server) createWorkflowNodeChildSession(
	parent *session.Session,
	def *workflowDefinitionRuntime,
	node workflowNodeRuntime,
) (*session.Session, error) {
	child, err := s.sessionManager.CreateWithParent(parent.AgentID, parent.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to create child session: %w", err)
	}
	if parent.ProjectID != nil {
		projectID := strings.TrimSpace(*parent.ProjectID)
		if projectID != "" {
			child.ProjectID = &projectID
		}
	}
	if child.Metadata == nil {
		child.Metadata = make(map[string]interface{})
	}
	child.Metadata["workflow_child"] = true
	child.Metadata["workflow_parent_id"] = parent.ID
	child.Metadata["workflow_node_id"] = node.ID
	child.Metadata["workflow_node_label"] = node.Label
	child.Metadata["workflow_name"] = def.Name

	if err := s.applyNodeRoutingMetadata(child, parent, node); err != nil {
		return child, err
	}
	if err := s.sessionManager.Save(child); err != nil {
		return child, fmt.Errorf("failed to save child session: %w", err)
	}
	return child, nil
}

func (s *Server) applyNodeRoutingMetadata(child *session.Session, parent *session.Session, node workflowNodeRuntime) error {
	parentProvider, parentModel := sessionProviderAndModel(parent)
	if parentProvider == "" {
		parentProvider = string(s.resolveSessionProviderType(parent))
	}
	if parentModel == "" {
		parentModel = s.resolveSessionModel(parent, config.ProviderType(parentProvider))
	}
	child.Metadata["provider"] = parentProvider
	if parentModel != "" {
		child.Metadata["model"] = parentModel
	}

	if strings.EqualFold(node.Kind, "subagent") {
		sa, err := s.resolveWorkflowSubAgent(node)
		if err != nil {
			return err
		}
		child.Metadata["sub_agent_id"] = sa.ID
		child.Metadata["sub_agent_name"] = sa.Name
		if strings.TrimSpace(sa.Provider) != "" {
			child.Metadata["provider"] = strings.TrimSpace(sa.Provider)
		}
		if strings.TrimSpace(sa.Model) != "" {
			child.Metadata["model"] = strings.TrimSpace(sa.Model)
		}
	}
	return nil
}

func (s *Server) resolveWorkflowSubAgent(node workflowNodeRuntime) (*storage.SubAgent, error) {
	idCandidates := []string{
		strings.TrimSpace(node.SubAgentID),
		strings.TrimSpace(node.Ref),
	}
	for _, candidate := range idCandidates {
		if candidate == "" {
			continue
		}
		if sa, err := s.store.GetSubAgent(candidate); err == nil && sa != nil {
			return sa, nil
		}
	}
	search := strings.ToLower(strings.TrimSpace(node.Label))
	if search == "" {
		search = strings.ToLower(strings.TrimSpace(node.Ref))
	}
	if search == "" {
		return nil, fmt.Errorf("sub-agent is missing for node %q", node.ID)
	}
	all, err := s.store.ListSubAgents()
	if err != nil {
		return nil, fmt.Errorf("failed to list sub-agents: %w", err)
	}
	for _, sa := range all {
		if strings.ToLower(strings.TrimSpace(sa.Name)) == search {
			return sa, nil
		}
	}
	return nil, fmt.Errorf("sub-agent not found for node %q", node.ID)
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
			ID:                 id,
			Label:              label,
			Kind:               kind,
			Ref:                strings.TrimSpace(asWorkflowString(row["ref"])),
			SubAgentID:         strings.TrimSpace(asWorkflowString(row["subAgentId"])),
			LocalAgentID:       strings.TrimSpace(asWorkflowString(row["localAgentId"])),
			ExternalAgentID:    strings.TrimSpace(asWorkflowString(row["externalAgentId"])),
			Instruction:        strings.TrimSpace(asWorkflowString(row["instruction"])),
			WorkerSubAgentID:   strings.TrimSpace(asWorkflowString(row["workerSubAgentId"])),
			WorkerLabel:        strings.TrimSpace(asWorkflowString(row["workerLabel"])),
			ReviewerSubAgentID: strings.TrimSpace(asWorkflowString(row["reviewerSubAgentId"])),
			ReviewerLabel:      strings.TrimSpace(asWorkflowString(row["reviewerLabel"])),
			LoopMaxTurns:       asWorkflowInt(row["loopMaxTurns"]),
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
				Instruction: "Produce the requested work for the review loop. Incorporate critic feedback from prior loop turns before handing off.",
			},
			workflowNodeRuntime{
				ID:          reviewerID,
				Label:       reviewerLabel,
				Kind:        "subagent",
				SubAgentID:  strings.TrimSpace(loop.ReviewerSubAgentID),
				Instruction: "Review the worker output. If it is acceptable, end with VERDICT: APPROVED. Otherwise give concrete revision feedback and end with VERDICT: REVISE.",
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

func workflowFinalOutput(def *workflowDefinitionRuntime, outputs map[string]string, succ map[string][]string) string {
	if def != nil && strings.EqualFold(strings.TrimSpace(def.Policy.StopCondition), "judge") {
		judgeID := strings.TrimSpace(def.Policy.JudgeNodeID)
		if judgeID != "" {
			if output := strings.TrimSpace(outputs[judgeID]); output != "" {
				return output
			}
		}
	}
	sinkIDs := make([]string, 0)
	for _, node := range def.Nodes {
		if strings.EqualFold(node.Kind, "user") {
			continue
		}
		if len(succ[node.ID]) == 0 {
			if strings.TrimSpace(outputs[node.ID]) != "" {
				sinkIDs = append(sinkIDs, node.ID)
			}
		}
	}
	if len(sinkIDs) == 0 {
		for _, node := range def.Nodes {
			if strings.EqualFold(node.Kind, "user") {
				continue
			}
			if strings.TrimSpace(outputs[node.ID]) != "" {
				sinkIDs = append(sinkIDs, node.ID)
			}
		}
	}
	sort.Strings(sinkIDs)
	if len(sinkIDs) == 0 {
		return "Workflow completed without output."
	}
	if len(sinkIDs) == 1 {
		return strings.TrimSpace(outputs[sinkIDs[0]])
	}
	parts := make([]string, 0, len(sinkIDs))
	for _, nodeID := range sinkIDs {
		out := strings.TrimSpace(outputs[nodeID])
		if out == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("### %s\n\n%s", nodeID, out))
	}
	if len(parts) == 0 {
		return "Workflow completed without output."
	}
	return strings.Join(parts, "\n\n")
}

func composeWorkflowNodePrompt(parent *session.Session, def *workflowDefinitionRuntime, node workflowNodeRuntime, userMessage string, upstreamOutputs []string, previousNodeOutput string) string {
	name := strings.TrimSpace(def.Name)
	if name == "" {
		name = strings.TrimSpace(def.ID)
	}
	var b strings.Builder
	b.WriteString("You are executing one node in a multi-agent workflow.\n")
	if name != "" {
		b.WriteString("Workflow: " + name + "\n")
	}
	b.WriteString("Node: " + strings.TrimSpace(node.Label) + "\n")
	if inst := strings.TrimSpace(node.Instruction); inst != "" {
		b.WriteString("\nNode instructions:\n")
		b.WriteString(inst)
		b.WriteString("\n")
	}
	if contextText := workflowParentSessionContext(parent, userMessage, 12, 12000); contextText != "" {
		b.WriteString("\nParent session context:\n")
		b.WriteString(contextText)
		b.WriteString("\n")
	}
	b.WriteString("\nCurrent user request:\n")
	b.WriteString(strings.TrimSpace(userMessage))
	b.WriteString("\n")
	if len(upstreamOutputs) > 0 {
		b.WriteString("\nInputs from previous nodes:\n")
		for idx, item := range upstreamOutputs {
			if strings.TrimSpace(item) == "" {
				continue
			}
			b.WriteString(fmt.Sprintf("\n[%d]\n%s\n", idx+1, strings.TrimSpace(item)))
		}
	}
	if strings.TrimSpace(previousNodeOutput) != "" {
		b.WriteString("\nPrevious output from this same node that was not accepted as a complete handoff:\n")
		b.WriteString(strings.TrimSpace(previousNodeOutput))
		b.WriteString("\n")
		b.WriteString("Continue from that state. Do not repeat the same progress update; perform the remaining work or explain the concrete blocker.\n")
		if workflowNodeRequiresToolEvidence(node, userMessage) {
			b.WriteString("Tools are available in this workflow node. If code changes are still needed, call the relevant tools now; do not report that you cannot edit merely because the previous response did not include tool calls.\n")
		}
	}
	if def != nil && strings.EqualFold(strings.TrimSpace(def.Policy.StopCondition), "judge") {
		judgeID := strings.TrimSpace(def.Policy.JudgeNodeID)
		if judgeID != "" && judgeID == strings.TrimSpace(node.ID) {
			b.WriteString("\nJudge node instruction:\n")
			b.WriteString("Add a final line exactly as `VERDICT: APPROVED` when work is acceptable, otherwise `VERDICT: REVISE`.\n")
		}
	}
	b.WriteString("\nWorkflow handoff status:\n")
	b.WriteString("Do the node's actual work before handing off. A plan, intention, summary of what you will do, or request to start work is not complete.\n")
	if workflowNodeRequiresToolEvidence(node, userMessage) {
		b.WriteString("For implementation nodes, use the available tools to inspect relevant files and make any needed edits before marking complete. You may call tools before producing this node's final textual output. If code changes are requested and you did not use a file editing tool, you must use `NODE_STATUS: IN_PROGRESS` unless there is a real external blocker. A response containing only `NODE_STATUS` is not useful progress; call tools or explain the concrete blocker.\n")
	}
	b.WriteString("End your response with a final line exactly `NODE_STATUS: COMPLETE` only when this node's concrete deliverable is ready for downstream review or use.\n")
	b.WriteString("Use `NODE_STATUS: IN_PROGRESS` if more implementation work remains, or `NODE_STATUS: BLOCKED` if you cannot proceed without user input or an external dependency.\n")
	b.WriteString("\nReturn only this node's output.")
	return b.String()
}

func workflowNodeWorkStatusForSession(node workflowNodeRuntime, output string, child *session.Session, userMessage string) string {
	status := workflowNodeWorkStatus(output)
	if !workflowNodeRequiresToolEvidence(node, userMessage) {
		return status
	}
	hasModificationActivity := workflowSessionHasModificationActivity(child)
	if status == "blocked" && !hasModificationActivity {
		if workflowOutputLooksLikeToolAvailabilityConfusion(output) || workflowOutputIsBareStatus(output) {
			return "in_progress"
		}
	}
	if status != "complete" {
		return status
	}
	if hasModificationActivity {
		return status
	}
	return "in_progress"
}

func workflowNodeRequiresToolEvidence(node workflowNodeRuntime, userMessage string) bool {
	if !workflowRequestLooksLikeToolWork(userMessage) {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(node.Kind))
	if kind != "main" && kind != "subagent" {
		return false
	}
	identity := strings.ToLower(strings.TrimSpace(node.Label + " " + node.ID + " " + node.Ref))
	if identity == "" {
		return true
	}
	return strings.Contains(identity, "build") ||
		strings.Contains(identity, "developer") ||
		strings.Contains(identity, "implement") ||
		strings.Contains(identity, "worker") ||
		strings.Contains(identity, "main")
}

func workflowRequestLooksLikeToolWork(userMessage string) bool {
	text := strings.ToLower(strings.TrimSpace(userMessage))
	if text == "" {
		return false
	}
	indicators := []string{
		"code",
		"repo",
		"repository",
		"file",
		"files",
		"function",
		"class",
		"component",
		"api",
		"endpoint",
		"database",
		"migration",
		"schema",
		"test",
		"tests",
		"bug",
		"fix",
		"implement",
		"refactor",
		"patch",
		"edit",
		"update",
		"typescript",
		"javascript",
		"react",
		"golang",
		"go ",
		"css",
		"html",
	}
	for _, indicator := range indicators {
		if strings.Contains(text, indicator) {
			return true
		}
	}
	return false
}

func workflowSessionHasModificationActivity(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	for _, msg := range sess.Messages {
		for _, call := range msg.ToolCalls {
			if workflowToolCanModifyFiles(call.Name) {
				return true
			}
		}
		for _, result := range msg.ToolResults {
			if workflowToolCanModifyFiles(result.Name) {
				return true
			}
		}
	}
	return false
}

func workflowToolCanModifyFiles(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "edit", "replace_lines", "insert_lines", "write":
		return true
	default:
		return false
	}
}

func workflowOutputLooksLikeToolAvailabilityConfusion(output string) bool {
	text := strings.ToLower(strings.TrimSpace(output))
	if text == "" {
		return false
	}
	toolMentions := []string{
		"no tool call",
		"no tool-call",
		"no tool calls",
		"no tool-calls",
		"не было ни одного tool",
		"не было tool",
		"нужен запуск инструмент",
		"нужны инструменты",
		"нет tool",
	}
	for _, mention := range toolMentions {
		if strings.Contains(text, mention) {
			return true
		}
	}
	return false
}

func workflowOutputIsBareStatus(output string) bool {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	meaningful := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		meaningful++
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "NODE_STATUS:") {
			return false
		}
	}
	return meaningful > 0
}

func workflowStateHasBlockedOrInProgressNode(state *workflowRuntimeState) bool {
	if state == nil {
		return false
	}
	for _, node := range state.Nodes {
		if node == nil {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(node.Status))
		if status == "blocked" || status == "in_progress" || status == "running" {
			return true
		}
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

func workflowNodeWorkStatus(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "NODE_STATUS:") {
			value := strings.TrimSpace(line[len("NODE_STATUS:"):])
			switch strings.ToUpper(value) {
			case "COMPLETE", "COMPLETED", "DONE":
				return "complete"
			case "IN_PROGRESS", "IN PROGRESS", "PROGRESS", "WORKING":
				return "in_progress"
			case "BLOCKED", "WAITING", "NEEDS_INPUT", "NEEDS INPUT":
				return "blocked"
			default:
				return "in_progress"
			}
		}
		break
	}
	return "complete"
}

func workflowParentSessionContext(parent *session.Session, currentUserMessage string, maxMessages int, maxChars int) string {
	if parent == nil || len(parent.Messages) == 0 || maxMessages <= 0 || maxChars <= 0 {
		return ""
	}
	messages := parent.Messages
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if strings.EqualFold(strings.TrimSpace(last.Role), "user") && strings.TrimSpace(last.Content) == strings.TrimSpace(currentUserMessage) {
			messages = messages[:len(messages)-1]
		}
	}
	if len(messages) == 0 {
		return ""
	}
	start := len(messages) - maxMessages
	if start < 0 {
		start = 0
	}
	parts := make([]string, 0, len(messages)-start)
	for _, msg := range messages[start:] {
		role := strings.TrimSpace(msg.Role)
		content := strings.TrimSpace(msg.Content)
		if role == "" || content == "" {
			continue
		}
		switch strings.ToLower(role) {
		case "user":
			role = "User"
		case "assistant":
			role = "Assistant"
		case "system":
			role = "System"
		default:
			role = strings.ToUpper(role[:1]) + role[1:]
		}
		parts = append(parts, fmt.Sprintf("%s: %s", role, content))
	}
	if len(parts) == 0 {
		return ""
	}
	text := strings.Join(parts, "\n\n")
	if len(text) <= maxChars {
		return text
	}
	return strings.TrimSpace(text[len(text)-maxChars:])
}

func preview(text string, max int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= max {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:max]) + "..."
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

func workflowMaxTurns(def *workflowDefinitionRuntime) int {
	if def == nil || def.Policy.MaxTurns <= 0 {
		return 12
	}
	return def.Policy.MaxTurns
}

func workflowTimeboxMinutes(def *workflowDefinitionRuntime) int {
	if def == nil || def.Policy.TimeboxMins <= 0 {
		return 20
	}
	return def.Policy.TimeboxMins
}

func workflowJudgeApproved(output string) bool {
	upper := strings.ToUpper(strings.TrimSpace(output))
	if upper == "" {
		return false
	}
	if strings.Contains(upper, "VERDICT: APPROVED") {
		return true
	}
	return strings.Contains(upper, "LGTM")
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
	runVersion map[string]int,
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
			depVersion := runVersion[dep]
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
