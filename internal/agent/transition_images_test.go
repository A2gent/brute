package agent

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestActivateTransitionSummaryKeepsEarlierImages(t *testing.T) {
	sess := session.New("a")
	sess.AddUserMessageWithImages("look", []session.ImageAttachment{{Name: "a.png", MediaType: "image/png", Path: "a.png"}})
	sess.AddAssistantMessage("ok", nil)
	sess.AddUserMessage("continue")
	(&Agent{}).activateTransitionSummary(sess, "summary", "t1")
	active := (&Agent{}).getActiveConversationMessages(sess)
	found := false
	for _, m := range active {
		if m.Role == "user" && len(m.Images) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("image lost after transition: %+v", active)
	}
}
