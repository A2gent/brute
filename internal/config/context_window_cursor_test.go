package config

import "testing"

func TestResolveContextWindowCursorComposer(t *testing.T) {
	if got := ResolveContextWindow(ProviderCursor, Provider{}, "composer-2.5"); got != 200000 {
		t.Fatalf("ResolveContextWindow(cursor, composer-2.5) = %d, want 200000", got)
	}
}
