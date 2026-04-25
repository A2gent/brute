package http

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestHasRunnableWorkflow(t *testing.T) {
	srv := &Server{}

	t.Run("does not run simple user-main workflow in fan-out mode", func(t *testing.T) {
		sess := session.New("build")
		sess.Metadata = map[string]interface{}{
			"workflow_definition": map[string]interface{}{
				"id": "wf-1",
				"nodes": []interface{}{
					map[string]interface{}{"id": "user", "kind": "user"},
					map[string]interface{}{"id": "main", "kind": "main"},
				},
			},
		}
		sess.AddUserMessage("initial request")

		if srv.hasRunnableWorkflow(sess) {
			t.Fatalf("expected simple user-main workflow to run directly in parent session")
		}
	})

	t.Run("runs multi-node workflow on initial turn", func(t *testing.T) {
		sess := session.New("build")
		sess.Metadata = map[string]interface{}{
			"workflow_definition": map[string]interface{}{
				"id": "wf-1",
				"nodes": []interface{}{
					map[string]interface{}{"id": "user", "kind": "user"},
					map[string]interface{}{"id": "worker-a", "kind": "subagent"},
					map[string]interface{}{"id": "worker-b", "kind": "main"},
				},
			},
		}
		sess.AddUserMessage("initial request")

		if !srv.hasRunnableWorkflow(sess) {
			t.Fatalf("expected fan-out workflow to run on initial turn")
		}
	})

	t.Run("reruns workflow after assistant has responded", func(t *testing.T) {
		sess := session.New("build")
		sess.Metadata = map[string]interface{}{
			"workflow_definition": map[string]interface{}{
				"id": "wf-1",
				"nodes": []interface{}{
					map[string]interface{}{"id": "user", "kind": "user"},
					map[string]interface{}{"id": "worker-a", "kind": "subagent"},
					map[string]interface{}{"id": "worker-b", "kind": "main"},
				},
			},
		}
		sess.AddUserMessage("initial request")
		sess.AddAssistantMessage("workflow result", nil)
		sess.AddUserMessage("follow-up question")

		if !srv.hasRunnableWorkflow(sess) {
			t.Fatalf("expected workflow fan-out to run on follow-up turns")
		}
	})

	t.Run("ignores workflow definitions without actionable nodes", func(t *testing.T) {
		sess := session.New("build")
		sess.Metadata = map[string]interface{}{
			"workflow_definition": map[string]interface{}{
				"id": "wf-1",
				"nodes": []interface{}{
					map[string]interface{}{"id": "user", "kind": "user"},
				},
			},
		}
		sess.AddUserMessage("initial request")

		if srv.hasRunnableWorkflow(sess) {
			t.Fatalf("expected non-actionable workflow to be skipped")
		}
	})
}
