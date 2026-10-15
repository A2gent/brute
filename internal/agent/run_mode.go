package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

const requestWriteToolName = "request_write"

// Use an allowlist: shell, arbitrary code, SQL, delegation and unknown integration
// tools can all write, even when their names do not look like file-editing tools.
func readOnlyToolAllowed(name string) bool {
	switch name {
	case "read", "grep", "glob", "find_files", "file_search", "content_search", "filter", "context_retrieve",
		"classify", "relevance_gate", "current_time", "random_number", "man", "parallel", "pipeline",
		"project_session_history", "archived_sessions", "brave_search_query", "exa_search", "tavily_search",
		"perplexity_search", "fetch_url", "youtube_transcript", requestWriteToolName:
		return true
	}
	return false
}

type requestWriteTool struct{}

func (requestWriteTool) Name() string { return requestWriteToolName }
func (requestWriteTool) Description() string {
	return "Request write access by stopping this read-only run and moving the session to its project's serial queue. Call before editing, committing, pushing, running commands or causing external effects. Work resumes automatically when the queue grants write access."
}
func (requestWriteTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (requestWriteTool) Execute(context.Context, json.RawMessage) (*tools.Result, error) {
	return &tools.Result{Success: true, Output: "Write access requested. This session will resume in the project serial queue."}, nil
}

func readOnlyExecutionContext(ctx context.Context, requested *atomic.Bool) context.Context {
	return tools.WithExecutionGuard(ctx, func(name string) error {
		if !readOnlyToolAllowed(name) {
			return fmt.Errorf("tool %s is unavailable in auto read-only mode; call request_write to continue in the project serial queue", name)
		}
		if name == requestWriteToolName {
			requested.Store(true)
		}
		return nil
	})
}

func (a *Agent) queueReadOnlyEscalation(sess *session.Session) error {
	sess.Metadata["queue_mode"] = "serial"
	sess.Metadata["queue_auto_start"] = true
	sess.Metadata[session.RunModeWriteRequestedKey] = true
	sess.SetStatus(session.StatusQueued)
	sess.AddAssistantMessage("Write access requested. The session is now in the project serial queue and will resume when write access is available.", nil)
	return a.sessionManager.Save(sess)
}

func readOnlyDefinitions(defs []llm.ToolDefinition) []llm.ToolDefinition {
	out := defs[:0]
	for _, def := range defs {
		if readOnlyToolAllowed(def.Name) {
			out = append(out, def)
		}
	}
	return out
}

// Native CLI tools bypass the manager guard; run those providers serially.
func readOnlyRequiresSerialRuntime(provider string) bool {
	return config.IsClaudeProviderRef(provider) || provider == string(config.ProviderKimiCLI) || provider == string(config.ProviderCursor) || provider == string(config.ProviderFallback) || config.IsFallbackAggregateRef(provider)
}

func (a *Agent) readOnlyNeedsSerialRuntime(sess *session.Session) bool {
	if !sess.IsAutoReadOnly() {
		return false
	}
	if readOnlyRequiresSerialRuntime(a.config.Provider) {
		return true
	}
	// Target-side transition compaction can invoke the native client before the
	// normal model step, so check the pending target before crossing that boundary.
	t := session.CurrentProviderTransition(sess)
	return t != nil && !t.Terminal() && readOnlyRequiresSerialRuntime(t.Target.Provider)
}
