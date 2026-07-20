package claudecli

import "strings"

const providerSessionCursorSeparator = "|"

// BindProviderSessionCursor returns an identity-bound opaque cursor when identity is set.
// Legacy callers with empty identity store the raw cursor unchanged.
func BindProviderSessionCursor(identity, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return raw
	}
	return identity + providerSessionCursorSeparator + raw
}

// ResolveProviderSessionCursor extracts the raw resume cursor when it is safe to use.
// Legacy unbound cursors are accepted only when identity is empty.
func ResolveProviderSessionCursor(identity, stored string) (raw string, ok bool) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return "", false
	}
	identity = strings.TrimSpace(identity)
	if !strings.Contains(stored, providerSessionCursorSeparator) {
		if identity != "" {
			return "", false
		}
		return stored, true
	}
	parts := strings.SplitN(stored, providerSessionCursorSeparator, 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	if identity == "" {
		return "", false
	}
	if parts[0] != identity {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

// UnbindProviderSessionCursor returns the raw cursor for persistence when possible.
func UnbindProviderSessionCursor(identity, stored string) string {
	if raw, ok := ResolveProviderSessionCursor(identity, stored); ok {
		return raw
	}
	return strings.TrimSpace(stored)
}
