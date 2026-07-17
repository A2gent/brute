package kimicli

import "strings"

func normalizeKimiCLIErrorMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return "Kimi CLI returned an error without details"
	}
	lower := strings.ToLower(msg)
	switch {
	case isKimiCLIConfigError(lower):
		return msg + "\nA2gent hint: Kimi CLI configuration is incomplete. Run `kimi login` or set default_model in ~/.kimi-code/config.toml."
	case isKimiCLIRateLimitError(lower):
		return msg + "\nA2gent hint: Kimi CLI hit a rate limit. Wait for the limit window to reset, lower concurrency, or switch the session/provider to a fallback model."
	case isKimiCLIAuthError(lower):
		return msg + "\nA2gent hint: Kimi CLI authentication is not ready. Run `kimi login` locally to sign in, then retry."
	default:
		return msg
	}
}

func isKimiCLIRateLimitError(lower string) bool {
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "429")
}

func isKimiCLIAuthError(lower string) bool {
	return strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "not authenticated") ||
		strings.Contains(lower, "login")
}

func isKimiCLIConfigError(lower string) bool {
	return strings.Contains(lower, "no model configured") ||
		strings.Contains(lower, "config.invalid") ||
		strings.Contains(lower, "not configured in config.toml")
}
