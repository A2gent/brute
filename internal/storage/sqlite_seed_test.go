package storage

import (
	"strings"
	"testing"
)

func TestBuiltInSpecificationSubAgentMaintainsBilingualFiles(t *testing.T) {
	for _, expected := range []string{
		"English source of truth",
		".ru.md",
		"Keep both language versions synchronized",
	} {
		if !strings.Contains(builtInSpecificationSubAgentPrompt, expected) {
			t.Fatalf("builtInSpecificationSubAgentPrompt must contain %q", expected)
		}
	}
	if strings.Contains(builtInSpecificationSubAgentPrompt, "single markdown specification file") {
		t.Fatal("builtInSpecificationSubAgentPrompt still restricts output to one markdown file")
	}
}
