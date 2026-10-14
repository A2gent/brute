package agent

import (
	"context"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"testing"
)

func TestTransitionSummaryProvenanceAndNoticeSurviveReload(t *testing.T) {
	target := &MockLLM{Responses: []*llm.ChatResponse{{Content: "summary"}, {Content: "answer"}}}
	a, sess, sm := transitionAgent(t, "compact_auto", target)
	originals := append([]session.Message(nil), sess.Messages...)
	anchor := session.CurrentProviderTransition(sess).Timeline.AfterMessageID
	if _, _, err := a.Run(context.Background(), sess, "ignored"); err != nil {
		t.Fatal(err)
	}
	restored, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	tr := session.CurrentProviderTransition(restored)
	if tr.Timeline.AfterMessageID != anchor || tr.TargetOutcome != "succeeded" {
		t.Fatalf("lost durable outcome/anchor: %+v", tr)
	}
	for i, original := range originals {
		if restored.Messages[i].ID != original.ID || restored.Messages[i].Content != original.Content {
			t.Fatal("original history changed")
		}
	}
	summary := restored.Messages[len(originals)]
	if summary.Provenance == nil || summary.Provenance.Provider != "target" || summary.Provenance.Model != "new" {
		t.Fatalf("summary attributed to source: %+v", summary)
	}
	for _, req := range target.CapturedRequests {
		for _, msg := range req.Messages {
			if msg.Content == "Provider transition" {
				t.Fatal("UI notice in LLM context")
			}
		}
	}
}

func TestStepLimitFinalizationRecordsTransitionOutcome(t *testing.T) {
	for _, content := range []string{"final answer", ""} {
		t.Run(content, func(t *testing.T) {
			target := &MockLLM{Response: &llm.ChatResponse{Content: content}}
			a, sess, sm := transitionAgent(t, "switch_auto", target)
			a.llmClient = target
			tr := session.CurrentProviderTransition(sess)
			tr.Phase = "continuing"
			tr.TargetOutcome = "selected"
			session.PutProviderTransition(sess, tr)
			_, _, err := a.finalizeAfterStepLimit(context.Background(), sess, 20, nil)
			restored, loadErr := sm.Get(sess.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			got := session.CurrentProviderTransition(restored)
			want := "succeeded"
			if content == "" {
				want = "failed"
				if err == nil {
					t.Fatal("empty answer accepted")
				}
			}
			if got.Phase != "completed" || got.TargetOutcome != want {
				t.Fatalf("stale finalization outcome: %+v", got)
			}
		})
	}
}
