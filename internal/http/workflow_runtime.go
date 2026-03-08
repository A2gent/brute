package http

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
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
	ID              string
	Label           string
	Kind            string
	Ref             string
	SubAgentID      string
	LocalAgentID    string
	ExternalAgentID string
	Instruction     string
}

type workflowEdgeRuntime struct {
	From string
	To   string
	Mode string
}

type workflowPolicyRuntime struct {
	StopCondition string
	JudgeNodeID   string
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
	err            error
}

func (s *Server) hasRunnableWorkflow(sess *session.Session) bool {
	def, ok := workflowDefinitionFromMetadata(sess)
	if !ok {
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
	completed := map[string]bool{}
	for _, node := range def.Nodes {
		if strings.EqualFold(node.Kind, "user") {
			completed[node.ID] = true
			outputs[node.ID] = userMessage
		}
	}
	pending := map[string]workflowNodeRuntime{}
	for _, node := range def.Nodes {
		if strings.EqualFold(node.Kind, "user") {
			continue
		}
		pending[node.ID] = node
	}

	for len(pending) > 0 {
		ready := make([]workflowNodeRuntime, 0, len(pending))
		for nodeID, node := range pending {
			nodeReady := true
			for _, dep := range preds[nodeID] {
				if !completed[dep] {
					nodeReady = false
					break
				}
			}
			if nodeReady {
				ready = append(ready, node)
			}
		}
		if len(ready) == 0 {
			state.Status = "failed"
			_ = s.persistWorkflowState(sess, state, emit)
			return "", llm.TokenUsage{}, fmt.Errorf("workflow graph has a cycle or unsatisfied dependencies")
		}

		sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
		results := make(chan workflowNodeResult, len(ready))
		var wg sync.WaitGroup
		for _, node := range ready {
			node := node
			st := state.Nodes[node.ID]
			if st != nil {
				st.Status = "running"
				st.StartedAt = time.Now().UTC().Format(time.RFC3339)
			}
			upstream := make([]string, 0, len(preds[node.ID]))
			for _, dep := range preds[node.ID] {
				if output := strings.TrimSpace(outputs[dep]); output != "" {
					upstream = append(upstream, output)
				}
			}
			if err := s.persistWorkflowState(sess, state, emit); err != nil {
				return "", llm.TokenUsage{}, err
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				output, childSessionID, err := s.executeWorkflowNode(ctx, sess, def, node, userMessage, upstream)
				results <- workflowNodeResult{
					nodeID:         node.ID,
					nodeLabel:      node.Label,
					childSessionID: childSessionID,
					output:         output,
					err:            err,
				}
			}()
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
			st.Status = "completed"
			st.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			st.OutputPreview = preview(result.output, 220)
			completed[result.nodeID] = true
			outputs[result.nodeID] = result.output
			delete(pending, result.nodeID)
		}
		if err := s.persistWorkflowState(sess, state, emit); err != nil {
			return "", llm.TokenUsage{}, err
		}
	}

	final := workflowFinalOutput(def, outputs, succ)
	state.Status = "completed"
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
) (string, string, error) {
	child, err := s.sessionManager.CreateWithParent(parent.AgentID, parent.ID)
	if err != nil {
		return "", "", fmt.Errorf("failed to create child session: %w", err)
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
		return "", child.ID, err
	}
	if err := s.sessionManager.Save(child); err != nil {
		return "", child.ID, fmt.Errorf("failed to save child session: %w", err)
	}

	nodePrompt := composeWorkflowNodePrompt(def, node, userMessage, upstreamOutputs)
	child.AddUserMessage(nodePrompt)
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
		SystemPrompt:  s.buildSystemPromptForSession(child),
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
			ID:              id,
			Label:           label,
			Kind:            kind,
			Ref:             strings.TrimSpace(asWorkflowString(row["ref"])),
			SubAgentID:      strings.TrimSpace(asWorkflowString(row["subAgentId"])),
			LocalAgentID:    strings.TrimSpace(asWorkflowString(row["localAgentId"])),
			ExternalAgentID: strings.TrimSpace(asWorkflowString(row["externalAgentId"])),
			Instruction:     strings.TrimSpace(asWorkflowString(row["instruction"])),
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
	return def, true
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

func composeWorkflowNodePrompt(def *workflowDefinitionRuntime, node workflowNodeRuntime, userMessage string, upstreamOutputs []string) string {
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
	b.WriteString("\nOriginal user request:\n")
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
	b.WriteString("\nReturn only this node's output.")
	return b.String()
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
