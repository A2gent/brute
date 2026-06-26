package session

import "testing"

func TestSummaryFromInitialPromptIsSingleConciseSentence(t *testing.T) {
	t.Parallel()

	got := SummaryFromContent("Please inspect the release blocker. Also check follow-up risks.")
	want := "Please inspect the release blocker."
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestRefreshSummaryUsesFinalAssistantForCompletedSession(t *testing.T) {
	t.Parallel()

	sess := New("build")
	sess.AddUserMessage("Investigate slow test suite and propose fixes")
	initial := sess.Summary
	if initial == "" {
		t.Fatal("expected initial summary from first user prompt")
	}

	sess.AddAssistantMessage("Found the slow integration tests and added targeted caching. Remaining risk is CI variance.", nil)
	sess.SetStatus(StatusCompleted)
	sess.RefreshSummary()

	want := "Found the slow integration tests and added targeted caching."
	if sess.Summary != want {
		t.Fatalf("summary = %q, want %q", sess.Summary, want)
	}
}

func TestSummaryFromContentCondensesLongMarkdown(t *testing.T) {
	t.Parallel()

	got := SummaryFromContent("### Done\n\n- Implemented session summaries across Brute and Caesar with migration support\n- Added tests")
	want := "Done - Implemented session summaries across Brute and Caesar with migration support - Added tests"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if len([]rune(got)) > maxSessionSummaryLength {
		t.Fatalf("summary length = %d, want <= %d", len([]rune(got)), maxSessionSummaryLength)
	}
}

func TestSummaryFromContentSkipsBareStatusSentence(t *testing.T) {
	t.Parallel()

	got := SummaryFromContent("Done. Implemented configurable session summaries and added tests.")
	want := "Implemented configurable session summaries and added tests."
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestRefreshSummaryDoesNotOverwriteWithBareStatus(t *testing.T) {
	t.Parallel()

	sess := New("build")
	sess.AddUserMessage("Improve session list summaries")
	sess.AddAssistantMessage("Done.", nil)
	sess.SetStatus(StatusCompleted)
	sess.RefreshSummary()

	want := "Improve session list summaries"
	if sess.Summary != want {
		t.Fatalf("summary = %q, want %q", sess.Summary, want)
	}
}
