package http

import (
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestResolveAgentMention(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	now := time.Now()
	yaml := "version: \"1\"\nagent:\n  id: reviewer\n  name: Reviewer\nruntime:\n  type: " + agentdef.RuntimeHost + "\n"
	if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: "reviewer", Name: "Reviewer", Runtime: agentdef.RuntimeHost, DefinitionYAML: yaml, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sess := session.New("build")

	m := server.resolveAgentMention(sess, "please @reviewer check this diff")
	if m == nil || m.AgentID != "reviewer" || m.Task != "please check this diff" {
		t.Fatalf("unexpected mention: %+v", m)
	}
	if server.resolveAgentMention(sess, "mail me at bob@reviewer.com") != nil {
		t.Fatal("embedded @ must not be a mention")
	}
	if server.resolveAgentMention(sess, "ping @nobody") != nil {
		t.Fatal("unknown agent must not be a mention")
	}
}

func TestMentionDelegationTaskIncludesHistory(t *testing.T) {
	sess := session.New("build")
	sess.AddUserMessage("how do I fix the bug?")
	task := mentionDelegationTask(sess, &agentMention{Task: "review this"})
	if !strings.Contains(task, "how do I fix the bug?") || !strings.HasSuffix(task, "review this") {
		t.Fatalf("task missing history or request: %q", task)
	}
}
