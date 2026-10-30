package agent

import (
	"strings"
	"testing"
)

func TestShortTransitionReasonDropsEchoedPrompt(t *testing.T) {
	got := shortTransitionReason("Cursor Agent CLI failed: Provider Error try again. You are running through Cursor Agent CLI. " + strings.Repeat("x", 5000))
	if got != "Cursor Agent CLI failed: Provider Error try again." {
		t.Fatalf("got %q", got)
	}
	if long := shortTransitionReason(strings.Repeat("a", 1000)); len(long) != maxTransitionReasonLen+3 {
		t.Fatalf("len %d", len(long))
	}
}

func TestFallbackTransitionModeSkipsConfirm(t *testing.T) {
	for in, want := range map[string]string{"": "compact_auto", "confirm": "compact_auto", "switch_auto": "switch_auto", "linked_session": "linked_session"} {
		if got := fallbackTransitionMode(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
