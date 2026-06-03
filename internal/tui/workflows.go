package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v2"
)

type tuiWorkflow struct {
	ID          string
	Name        string
	Description string
	BuiltIn     bool
	Definition  map[string]interface{}
}

// New creates a new TUI model
func (m Model) showAgentsSelection() (tea.Model, tea.Cmd) {
	workflows, err := m.loadWorkflows()
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to list workflows: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}
	if len(workflows) == 0 {
		workflows = []tuiWorkflow{builtinUserMainWorkflow()}
	}

	m.availableWorkflows = workflows
	m.agentsMenuIndex = 0
	m.showAgentsMenu = true

	currentID := m.selectedWorkflow.ID
	if sessionWorkflow := workflowFromSessionMetadata(m.session); sessionWorkflow.ID != "" {
		currentID = sessionWorkflow.ID
	}
	for i, workflow := range workflows {
		if workflow.ID == currentID {
			m.agentsMenuIndex = i
			break
		}
	}

	return m, nil
}

func (m Model) selectWorkflowForNewSession() (tea.Model, tea.Cmd) {
	if len(m.availableWorkflows) == 0 || m.agentsMenuIndex < 0 || m.agentsMenuIndex >= len(m.availableWorkflows) {
		m.showAgentsMenu = false
		return m, nil
	}
	workflow := m.availableWorkflows[m.agentsMenuIndex]
	m.showAgentsMenu = false
	return m.createNewSessionWithWorkflow(workflow)
}
func builtinUserMainWorkflow() tuiWorkflow {
	def := map[string]interface{}{
		"id":          "builtin:user-main",
		"name":        "User <-> Agent",
		"description": "Default chat between you and one agent.",
		"entryNodeId": "n-user",
		"policy": map[string]interface{}{
			"maxTurns":       12,
			"stopCondition":  "manual",
			"timeboxMinutes": 20,
		},
		"nodes": []interface{}{
			map[string]interface{}{"id": "n-user", "label": "User", "kind": "user"},
			map[string]interface{}{"id": "n-main", "label": "Main agent", "kind": "main"},
		},
		"edges": []interface{}{
			map[string]interface{}{"from": "n-user", "to": "n-main", "mode": "sequential"},
		},
	}
	return tuiWorkflow{
		ID:          "builtin:user-main",
		Name:        "User <-> Agent",
		Description: "Default chat between you and one agent.",
		BuiltIn:     true,
		Definition:  def,
	}
}

func (m Model) loadWorkflows() ([]tuiWorkflow, error) {
	workflows := []tuiWorkflow{builtinUserMainWorkflow()}
	projects, err := m.sessionManager.ListProjects()
	if err != nil {
		return workflows, err
	}

	var workflowProject *session.Project
	for _, project := range projects {
		if project == nil || project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
			continue
		}
		if project.ID == "system-soul" {
			workflowProject = project
			break
		}
		if project.IsSystem && strings.EqualFold(strings.TrimSpace(project.Name), "Soul") {
			workflowProject = project
		}
	}
	if workflowProject == nil || workflowProject.Folder == nil {
		return workflows, nil
	}

	root := filepath.Join(*workflowProject.Folder, "workflows")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return workflows, nil
		}
		return workflows, err
	}

	seen := map[string]bool{"builtin:user-main": true}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".md") && !strings.HasSuffix(lower, ".markdown") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		workflow, ok := parseWorkflowFile(content)
		if !ok || workflow.ID == "" || seen[workflow.ID] {
			continue
		}
		seen[workflow.ID] = true
		workflows = append(workflows, workflow)
	}

	sort.SliceStable(workflows, func(i, j int) bool {
		if workflows[i].BuiltIn != workflows[j].BuiltIn {
			return workflows[i].BuiltIn
		}
		return strings.ToLower(workflows[i].Name) < strings.ToLower(workflows[j].Name)
	})
	return workflows, nil
}

func parseWorkflowFile(content []byte) (tuiWorkflow, bool) {
	var raw interface{}
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return tuiWorkflow{}, false
	}
	converted, ok := convertYAMLValue(raw).(map[string]interface{})
	if !ok {
		return tuiWorkflow{}, false
	}
	id := strings.TrimSpace(asWorkflowString(converted["id"]))
	if id == "" || strings.HasPrefix(id, "builtin:") {
		return tuiWorkflow{}, false
	}
	nodes := asWorkflowSlice(converted["nodes"])
	if len(nodes) == 0 {
		return tuiWorkflow{}, false
	}
	name := strings.TrimSpace(asWorkflowString(converted["name"]))
	if name == "" {
		name = id
	}
	description := asWorkflowString(converted["description"])
	converted["id"] = id
	converted["name"] = name
	converted["description"] = description
	converted["nodes"] = nodes
	if _, ok := converted["edges"]; !ok {
		converted["edges"] = []interface{}{}
	}
	if _, ok := converted["entryNodeId"]; !ok {
		if first, ok := nodes[0].(map[string]interface{}); ok {
			converted["entryNodeId"] = asWorkflowString(first["id"])
		}
	}
	if _, ok := converted["policy"]; !ok {
		converted["policy"] = map[string]interface{}{
			"maxTurns":       12,
			"stopCondition":  "manual",
			"timeboxMinutes": 20,
		}
	}
	return tuiWorkflow{
		ID:          id,
		Name:        name,
		Description: description,
		BuiltIn:     false,
		Definition:  converted,
	}, true
}

func convertYAMLValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[interface{}]interface{}:
		next := make(map[string]interface{}, len(v))
		for key, item := range v {
			next[fmt.Sprint(key)] = convertYAMLValue(item)
		}
		return next
	case map[string]interface{}:
		next := make(map[string]interface{}, len(v))
		for key, item := range v {
			next[key] = convertYAMLValue(item)
		}
		return next
	case []interface{}:
		next := make([]interface{}, 0, len(v))
		for _, item := range v {
			next = append(next, convertYAMLValue(item))
		}
		return next
	default:
		return v
	}
}

func asWorkflowString(raw interface{}) string {
	if raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func asWorkflowSlice(raw interface{}) []interface{} {
	switch v := raw.(type) {
	case []interface{}:
		return v
	default:
		return nil
	}
}

func workflowFromSessionMetadata(sess *session.Session) tuiWorkflow {
	if sess == nil || sess.Metadata == nil {
		return tuiWorkflow{}
	}
	id := strings.TrimSpace(asWorkflowString(sess.Metadata["workflow_id"]))
	name := strings.TrimSpace(asWorkflowString(sess.Metadata["workflow_name"]))
	def, _ := sess.Metadata["workflow_definition"].(map[string]interface{})
	if id == "" && def != nil {
		id = strings.TrimSpace(asWorkflowString(def["id"]))
	}
	if name == "" && def != nil {
		name = strings.TrimSpace(asWorkflowString(def["name"]))
	}
	if id == "" && name == "" {
		return tuiWorkflow{}
	}
	if name == "" {
		name = id
	}
	description := ""
	if def != nil {
		description = asWorkflowString(def["description"])
	}
	return tuiWorkflow{
		ID:          id,
		Name:        name,
		Description: description,
		BuiltIn:     strings.HasPrefix(id, "builtin:"),
		Definition:  def,
	}
}

func applyWorkflowMetadata(sess *session.Session, workflow tuiWorkflow) {
	if sess == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	if workflow.ID == "" {
		workflow = builtinUserMainWorkflow()
	}
	metadata := buildWorkflowSessionMetadata(workflow)
	for key, value := range metadata {
		sess.Metadata[key] = value
	}
}

func buildWorkflowSessionMetadata(workflow tuiWorkflow) map[string]interface{} {
	def := workflow.Definition
	if def == nil {
		def = builtinUserMainWorkflow().Definition
	}
	id := strings.TrimSpace(asWorkflowString(def["id"]))
	if id == "" {
		id = workflow.ID
	}
	name := strings.TrimSpace(asWorkflowString(def["name"]))
	if name == "" {
		name = workflow.Name
	}
	description := asWorkflowString(def["description"])
	nodes := normalizeWorkflowNodes(def["nodes"])
	edges := normalizeWorkflowEdges(def["edges"])

	workflowDef := map[string]interface{}{
		"id":          id,
		"name":        name,
		"description": description,
		"entryNodeId": asWorkflowString(def["entryNodeId"]),
		"policy":      def["policy"],
		"nodes":       nodes,
		"edges":       edges,
	}
	return map[string]interface{}{
		"workflow_id":         id,
		"workflow_name":       name,
		"workflow_definition": workflowDef,
	}
}

func normalizeWorkflowNodes(raw interface{}) []interface{} {
	nodes := asWorkflowSlice(raw)
	out := make([]interface{}, 0, len(nodes))
	for _, item := range nodes {
		node, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		kind := asWorkflowString(node["kind"])
		ref := ""
		switch kind {
		case "subagent":
			ref = asWorkflowString(node["subAgentId"])
		case "local":
			ref = asWorkflowString(node["localAgentId"])
		case "external":
			ref = asWorkflowString(node["externalAgentId"])
		}
		out = append(out, map[string]interface{}{
			"id":                  asWorkflowString(node["id"]),
			"label":               asWorkflowString(node["label"]),
			"kind":                kind,
			"ref":                 ref,
			"subAgentId":          node["subAgentId"],
			"localAgentId":        node["localAgentId"],
			"externalAgentId":     node["externalAgentId"],
			"instruction":         node["instruction"],
			"workerSubAgentId":    node["workerSubAgentId"],
			"workerLabel":         node["workerLabel"],
			"workerInstruction":   node["workerInstruction"],
			"reviewerSubAgentId":  node["reviewerSubAgentId"],
			"reviewerLabel":       node["reviewerLabel"],
			"reviewerInstruction": node["reviewerInstruction"],
			"loopMaxTurns":        node["loopMaxTurns"],
		})
	}
	return out
}

func normalizeWorkflowEdges(raw interface{}) []interface{} {
	edges := asWorkflowSlice(raw)
	out := make([]interface{}, 0, len(edges))
	for _, item := range edges {
		edge, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		out = append(out, map[string]interface{}{
			"from": asWorkflowString(edge["from"]),
			"to":   asWorkflowString(edge["to"]),
			"mode": asWorkflowString(edge["mode"]),
		})
	}
	return out
}

func workflowLaunchLabel(workflow tuiWorkflow) string {
	def := workflow.Definition
	if def == nil {
		return workflow.Name
	}
	nodes := asWorkflowSlice(def["nodes"])
	if len(nodes) == 0 {
		return workflow.Name
	}
	nodeByID := make(map[string]map[string]interface{}, len(nodes))
	for _, item := range nodes {
		node, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id := asWorkflowString(node["id"])
		if id != "" {
			nodeByID[id] = node
		}
	}
	toLabel := func(node map[string]interface{}) string {
		if node == nil {
			return ""
		}
		label := strings.TrimSpace(asWorkflowString(node["label"]))
		if label == "" {
			label = strings.TrimSpace(asWorkflowString(node["id"]))
		}
		kind := strings.TrimSpace(asWorkflowString(node["kind"]))
		if kind != "" && label != "" {
			return fmt.Sprintf("%s (%s)", label, kind)
		}
		return label
	}
	launchable := func(node map[string]interface{}) bool {
		switch strings.TrimSpace(asWorkflowString(node["kind"])) {
		case "main", "subagent", "local", "external", "review_loop":
			return true
		default:
			return false
		}
	}

	entryID := asWorkflowString(def["entryNodeId"])
	if entry := nodeByID[entryID]; entry != nil {
		if launchable(entry) {
			return toLabel(entry)
		}
		for _, item := range asWorkflowSlice(def["edges"]) {
			edge, ok := item.(map[string]interface{})
			if !ok || asWorkflowString(edge["from"]) != entryID {
				continue
			}
			if node := nodeByID[asWorkflowString(edge["to"])]; launchable(node) {
				return toLabel(node)
			}
		}
	}
	for _, node := range nodeByID {
		if launchable(node) {
			return toLabel(node)
		}
	}
	return workflow.Name
}
