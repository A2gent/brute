package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

const (
	// metadataToolGroups stores the sticky optional tool groups of a session.
	// Absent means "all tools" (feature off or legacy session).
	metadataToolGroups        = "tool_groups"
	defaultToolGroupThreshold = 0.3
	toolGroupSelectionTimeout = 3 * time.Second
)

// ToolSelector enables Jev-based tool-group selection. Nil disables it.
type ToolSelector struct {
	Client    *jev.Client
	Secrets   []string
	Threshold float64
}

// ensureToolGroups picks the session's tool groups once, on the first user
// prompt. The choice is stored on the session so the tool list stays
// byte-identical across steps and runs (prompt caching). Every failure stores
// all groups: fail-open, but still sticky so Jev is not retried each step.
func (a *Agent) ensureToolGroups(ctx context.Context, sess *session.Session) {
	selector := a.config.ToolSelector
	if selector == nil || selector.Client == nil || sess == nil {
		return
	}
	if _, ok := sess.Metadata[metadataToolGroups]; ok {
		return
	}
	prompt := ""
	for _, msg := range sess.Messages {
		if msg.Role == "assistant" {
			// Legacy session already running with all tools: keep it that way.
			return
		}
		if msg.Role == "user" && prompt == "" {
			prompt = strings.TrimSpace(msg.Content)
		}
	}
	groups := tools.ToolGroupNames()
	if prompt != "" && jev.SafeEgressText(prompt, selector.Secrets) {
		threshold := selector.Threshold
		if threshold <= 0 {
			threshold = defaultToolGroupThreshold
		}
		selectCtx, cancel := context.WithTimeout(ctx, toolGroupSelectionTimeout)
		started := time.Now()
		selected, err := tools.SelectToolGroups(selectCtx, selector.Client, prompt, threshold)
		cancel()
		outcome := "success"
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = "timeout"
		} else if err != nil {
			outcome = "error"
		}
		recordAuxiliaryCall(sess, "tool_selection", started, outcome)
		if err != nil {
			logging.Warn("Tool group selection failed for session %s, enabling all tools: %v", sess.ID, err)
		} else {
			groups = selected
		}
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata[metadataToolGroups] = groups
	logging.Info("Tool groups for session %s: %v", sess.ID, groups)
}

// activeToolGroups returns the enabled groups and whether the tool list is
// restricted at all. enable_tools results from the transcript are folded into
// the stored set, which survives later compaction of those messages.
func activeToolGroups(sess *session.Session) (map[string]bool, bool) {
	if sess == nil || sess.Metadata == nil {
		return nil, false
	}
	raw, ok := sess.Metadata[metadataToolGroups]
	if !ok {
		return nil, false
	}
	active := map[string]bool{}
	for _, name := range stringList(raw) {
		active[name] = true
	}
	changed := false
	for _, msg := range sess.Messages {
		for _, tr := range msg.ToolResults {
			if tr.Name != tools.EnableToolsToolName || tr.IsError {
				continue
			}
			for _, name := range stringList(tr.Metadata[tools.EnableToolGroupsMetadataKey]) {
				if !active[name] {
					active[name] = true
					changed = true
				}
			}
		}
	}
	if changed {
		groups := make([]string, 0, len(active))
		for _, name := range tools.ToolGroupNames() {
			if active[name] {
				groups = append(groups, name)
			}
		}
		sess.Metadata[metadataToolGroups] = groups
	}
	for _, name := range tools.ToolGroupNames() {
		if !active[name] {
			return active, true
		}
	}
	return active, false
}

// toolDefinitions filters the manager's tools by the session's groups. With
// nothing hidden, enable_tools is dropped because it would have no effect.
func (a *Agent) toolDefinitions(sess *session.Session) []llm.ToolDefinition {
	defs := a.toolManager.GetDefinitions()
	active, restricted := activeToolGroups(sess)
	if sess.IsAutoReadOnly() {
		defs = readOnlyDefinitions(defs)
	}
	out := defs[:0]
	for _, def := range defs {
		if def.Name == requestWriteToolName && !sess.IsAutoReadOnly() {
			continue
		}
		if def.Name == tools.EnableToolsToolName {
			if restricted {
				out = append(out, def)
			}
			continue
		}
		if group := tools.ToolGroupOf(def.Name); !restricted || group == "" || active[group] {
			out = append(out, def)
		}
	}
	return out
}

// stringList accepts both in-memory []string and JSON-decoded []interface{}.
func stringList(raw interface{}) []string {
	switch values := raw.(type) {
	case []string:
		return values
	case []interface{}:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}
