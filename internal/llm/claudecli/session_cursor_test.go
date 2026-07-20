package claudecli

import "testing"

func TestResolveProviderSessionCursorMismatchedBoundCursorNoResume(t *testing.T) {
	t.Parallel()

	if raw, ok := ResolveProviderSessionCursor("claude:abc", "claude:other|cursor-1"); ok || raw != "" {
		t.Fatalf("mismatched bound cursor should not resume: raw=%q ok=%v", raw, ok)
	}
}

func TestResolveProviderSessionCursorLegacyUnboundOnlyWhenIdentityEmpty(t *testing.T) {
	t.Parallel()

	raw, ok := ResolveProviderSessionCursor("", "cursor-legacy")
	if !ok || raw != "cursor-legacy" {
		t.Fatalf("legacy unbound = %q ok=%v", raw, ok)
	}
	if raw, ok := ResolveProviderSessionCursor("claude:abc", "cursor-legacy"); ok || raw != "" {
		t.Fatalf("legacy cursor with configured identity must not resume: raw=%q ok=%v", raw, ok)
	}
	if raw, ok := ResolveProviderSessionCursor("", "claude:abc|cursor-1"); ok || raw != "" {
		t.Fatalf("legacy client must reject identity-bound cursor: raw=%q ok=%v", raw, ok)
	}
}

func TestBindAndResolveProviderSessionCursorRoundTrip(t *testing.T) {
	t.Parallel()

	bound := BindProviderSessionCursor("claude:abc", "cursor-1")
	raw, ok := ResolveProviderSessionCursor("claude:abc", bound)
	if !ok || raw != "cursor-1" {
		t.Fatalf("round trip = %q ok=%v", raw, ok)
	}
}
