package http

import (
	"context"

	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/agent"

	"github.com/A2gent/brute/internal/llm"

	"github.com/A2gent/brute/internal/session"
)

func (s *Server) hasRunnableWorkflow(sess *session.Session) bool {
	def, ok := workflowDefinitionFromMetadataWithTemplates(sess, s.loadPromptTemplates())
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
	templates := s.loadPromptTemplates()
	def, ok := workflowDefinitionFromMetadataWithTemplates(sess, templates)
	if !ok {
		return "", llm.TokenUsage{}, fmt.Errorf("workflow metadata is missing")
	}
	graph := newWorkflowGraph(def)

	previousState := workflowRuntimeStateFromMetadata(sess)
	state := &workflowRuntimeState{
		WorkflowID:   def.ID,
		WorkflowName: def.Name,
		Status:       "running",
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		Nodes:        make(map[string]*workflowRuntimeNodeState, len(def.Nodes)),
	}
	for _, node := range def.Nodes {
		st := &workflowRuntimeNodeState{Status: "pending"}
		if previousState != nil && previousState.Nodes != nil {
			if previousNodeState := previousState.Nodes[node.ID]; previousNodeState != nil {
				st.ChildSessionID = strings.TrimSpace(previousNodeState.ChildSessionID)
			}
		}
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
	enforceTurnCap := graph.HasCycle || stopCondition == "max_turns"
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
		ready := workflowReadyNodes(actionable, graph.Preds, completeVersion, retryRequested, nodeTurnState, graph.SCCByNode)
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
			upstream := make([]string, 0, len(graph.Preds[node.ID]))
			for _, dep := range graph.Preds[node.ID] {
				version := completeVersion[dep]
				lastConsumed := 0
				if ts != nil {
					lastConsumed = ts.LastConsumedByDep[dep]
				}
				if version > 0 && ts != nil {
					ts.LastConsumedByDep[dep] = version
				}
				if output := strings.TrimSpace(outputs[dep]); output != "" && version > lastConsumed {
					upstream = append(upstream, output)
				}
			}
			previousNodeOutput := ""
			if wasRetry {
				previousNodeOutput = strings.TrimSpace(outputs[node.ID])
				if previousNodeOutput == "" {
					previousNodeOutput = workflowBareStatusRetryPromptWithTemplate(node, templates.WorkflowBareStatusRetryPromptTemplate)
				}
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
				modificationActivityBefore := workflowSessionModificationActivityCount(child)
				output, childSessionID, err := s.executeWorkflowNode(ctx, sess, def, node, userMessage, upstream, previousNodeOutput, child, templates)
				modificationActivityAfter := workflowSessionModificationActivityCount(child)
				cleanOutput := workflowCleanNodeOutputForHandoff(output)
				emptyHandoff := strings.TrimSpace(cleanOutput) == ""
				if emptyHandoff {
					cleanOutput = workflowLatestMeaningfulAssistantOutput(child)
				}
				if strings.TrimSpace(cleanOutput) == "" {
					cleanOutput = workflowCleanNodeOutputForHandoff(previousNodeOutput)
				}
				results <- workflowNodeResult{
					nodeID:                  node.ID,
					nodeLabel:               node.Label,
					childSessionID:          childSessionID,
					output:                  cleanOutput,
					emptyHandoff:            emptyHandoff,
					newModificationActivity: modificationActivityAfter > modificationActivityBefore,
					workStatus:              workflowNodeWorkStatusForSession(node, output, child, userMessage),
					err:                     err,
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
				if result.emptyHandoff && nodeTurnState[result.nodeID] != nil && nodeTurnState[result.nodeID].RunCount > 0 && !result.newModificationActivity {
					st.Status = "blocked"
					st.Error = "Node returned only workflow status on retry without producing a new usable handoff or file edit."
				} else {
					st.Status = "in_progress"
					retryRequested[result.nodeID] = true
				}
			case "blocked":
				st.Status = "blocked"
			default:
				st.Status = "completed"
			}
			st.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			st.OutputPreview = preview(result.output, 220)
			outputs[result.nodeID] = result.output
			entryTurn := runVersion[result.nodeID] + 1
			runVersion[result.nodeID]++
			if cleanTranscriptOutput := strings.TrimSpace(result.output); cleanTranscriptOutput != "" {
				node := graph.NodeByID[result.nodeID]
				nodeLabel := strings.TrimSpace(result.nodeLabel)
				if nodeLabel == "" {
					nodeLabel = strings.TrimSpace(node.Label)
				}
				entry := workflowTranscriptEntry{
					ID:             fmt.Sprintf("%s:%s:%d", sess.ID, result.nodeID, entryTurn),
					NodeID:         result.nodeID,
					NodeLabel:      nodeLabel,
					NodeKind:       node.Kind,
					ChildSessionID: result.childSessionID,
					Role:           "agent",
					Content:        cleanTranscriptOutput,
					CreatedAt:      st.CompletedAt,
					Status:         st.Status,
					Turn:           entryTurn,
				}
				if err := s.appendWorkflowTranscriptEntry(sess, entry, emit); err != nil {
					return "", llm.TokenUsage{}, err
				}
			}
			if result.workStatus == "complete" {
				completeVersion[result.nodeID]++
			}
			if ts := nodeTurnState[result.nodeID]; ts != nil {
				ts.RunCount++
			}
			s.syncWorkflowChildSessionStatus(result.childSessionID, st.Status)
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

	workflowMarkUnfinishedNodesBlocked(state, exitReason)

	unreachable := workflowUnreachedActionableNodes(actionable, runVersion)
	blockedByNeverRunDeps := workflowNodesBlockedByNeverRunDeps(unreachable, graph.Preds, runVersion, graph.SCCByNode)
	if exitReason == "no_ready" && len(blockedByNeverRunDeps) > 0 {
		diagnostic := workflowPendingDependencyDiagnostic(blockedByNeverRunDeps, graph.Preds, runVersion, graph.SCCByNode)
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

	final := workflowFinalOutput(def, outputs, graph.Succ)
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
	if strings.EqualFold(state.Status, "blocked") {
		final = workflowBlockedFinalOutput(def, state)
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

func (s *Server) appendWorkflowTranscriptEntry(sess *session.Session, entry workflowTranscriptEntry, emit func(ChatStreamEvent) bool) error {
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	entries := workflowTranscriptEntriesFromMetadata(sess.Metadata[workflowTranscriptMetadataKey])
	for _, existing := range entries {
		if existing.ID == entry.ID {
			return nil
		}
	}
	entries = append(entries, entry)
	sess.Metadata[workflowTranscriptMetadataKey] = entries
	if err := s.sessionManager.Save(sess); err != nil {
		return err
	}
	if emit != nil {
		_ = emit(ChatStreamEvent{Type: "workflow_transcript_entry", WorkflowTranscriptEntry: entry})
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
	templates serverPromptTemplates,
) (string, string, error) {
	if child == nil {
		return "", "", fmt.Errorf("workflow child session is nil")
	}

	fullContext := !workflowChildContextSeeded(child)
	nodePrompt := composeWorkflowNodePromptForChildWithTemplate(parent, def, node, userMessage, upstreamOutputs, previousNodeOutput, child, fullContext, templates.WorkflowNodePromptTemplate)
	child.AddUserMessageWithImagesAndMetadata(nodePrompt, nil, workflowNodePromptMessageMetadata(parent, def, node))
	if child.Metadata == nil {
		child.Metadata = make(map[string]interface{})
	}
	child.Metadata[workflowContextSeededKey] = true
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
		Name:                child.AgentID,
		Provider:            string(target.ProviderType),
		Model:               target.Model,
		SystemPrompt:        s.buildSystemPromptForWorkflowNode(child, node),
		MaxSteps:            s.config.MaxSteps,
		Temperature:         s.config.Temperature,
		ContextWindow:       target.ContextWindow,
		UsePreviousResponse: target.StatefulResponses,
	}
	ag := s.newAgentFromConfig(agentConfig, target.Client, s.toolManagerForWorkflowNode(child, node))
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

func workflowMarkUnfinishedNodesBlocked(state *workflowRuntimeState, exitReason string) {
	if state == nil {
		return
	}
	reason := workflowExitReasonMessage(exitReason)
	if reason == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, node := range state.Nodes {
		if node == nil {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(node.Status))
		if status != "in_progress" && status != "running" {
			continue
		}
		node.Status = "blocked"
		node.CompletedAt = now
		if strings.TrimSpace(node.Error) == "" {
			node.Error = reason
		}
	}
}

func workflowExitReasonMessage(exitReason string) string {
	switch strings.ToLower(strings.TrimSpace(exitReason)) {
	case "turn_cap":
		return "Workflow turn limit reached before this node produced a complete handoff."
	case "timebox":
		return "Workflow timebox expired before this node produced a complete handoff."
	case "no_ready":
		return "Workflow has no ready nodes left, but this node did not produce a complete handoff."
	default:
		return ""
	}
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
