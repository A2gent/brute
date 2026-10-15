package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHeadTailKeepsBothEndsWithinBudget(t *testing.T) {
	text := "START" + strings.Repeat("привет", 1000) + "END"
	got := HeadTail(text, 101)
	if len(got) > 101 || !utf8.ValidString(got) || !strings.HasPrefix(got, "START") || !strings.HasSuffix(got, "END") || !strings.Contains(got, "[truncated]") {
		t.Fatalf("HeadTail = %q (%d bytes)", got, len(got))
	}
	if HeadTail("short", 101) != "short" {
		t.Fatal("short text must be unchanged")
	}
}
