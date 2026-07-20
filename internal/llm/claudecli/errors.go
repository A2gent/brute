package claudecli

import (
	"encoding/json"
	"strings"
)

func cliErrorMessage(runErr error, stdout, stderr string) string {
	parts := make([]string, 0, 3)
	if stderr = strings.TrimSpace(stderr); stderr != "" {
		parts = append(parts, stderr)
	}
	if stdout = strings.TrimSpace(stdout); stdout != "" {
		if msg := cliOutputMessage(stdout); msg != "" {
			parts = append(parts, msg)
		} else {
			parts = append(parts, stdout)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, runErr.Error())
	}
	return strings.Join(parts, "\n")
}

func cliOutputMessage(stdout string) string {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return ""
	}
	if json.Valid([]byte(stdout)) {
		if parsed, _, err := parseCLIResult(stdout); err == nil {
			return cliResultMessage(parsed)
		}
	}

	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if event, err := parseCLIStreamEnvelope(line); err == nil {
			if msg := cliStreamEnvelopeMessage(event); msg != "" {
				return msg
			}
		}
		if parsed, _, err := parseCLIResult(line); err == nil {
			if msg := cliResultMessage(parsed); msg != "" {
				return msg
			}
		}
	}
	return ""
}

func cliResultMessage(parsed cliResult) string {
	return firstNonEmpty(parsed.Error, parsed.Result, parsed.Message)
}

func cliStreamEnvelopeMessage(event cliStreamEnvelope) string {
	return firstNonEmpty(event.Error, event.Result, event.MessageText, streamMessageText(event.Message))
}

func normalizeClaudeCLIErrorMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return "Claude CLI returned an error without details"
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "a2gent hint:") {
		return msg
	}

	switch {
	case isClaudeCLIRateLimitError(lower):
		return msg + "\nA2gent hint: Claude CLI hit a rate limit. Wait for the limit window to reset, lower concurrency, or switch the session/provider to a fallback model."
	case isClaudeCLICreditsError(lower):
		return msg + "\nA2gent hint: Claude CLI reported a credits, quota, billing, or budget problem. Check Claude billing/plan credits and AAGENT_CLAUDE_CLI_MAX_BUDGET_USD."
	case isClaudeCLIPermissionError(lower):
		return msg + "\nA2gent hint: Claude CLI could not proceed because a tool permission was denied or required an interactive prompt. Use a non-interactive permission mode such as AAGENT_CLAUDE_CLI_PERMISSION_MODE=acceptEdits or allow the needed Claude Code tools."
	case isClaudeCLIAuthError(lower):
		return msg + "\nA2gent hint: Claude CLI authentication is not ready. Run Claude Code locally to sign in, or check the account/token used by the Claude CLI."
	default:
		return msg
	}
}

func isClaudeCLIRateLimitError(lower string) bool {
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "ratelimit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "429")
}

func isClaudeCLICreditsError(lower string) bool {
	return strings.Contains(lower, "out of credits") ||
		strings.Contains(lower, "no credits") ||
		strings.Contains(lower, "insufficient credits") ||
		strings.Contains(lower, "credit balance") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "billing") ||
		strings.Contains(lower, "payment required") ||
		strings.Contains(lower, "402") ||
		strings.Contains(lower, "max budget")
}

func isClaudeCLIPermissionError(lower string) bool {
	return strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "requires permission") ||
		strings.Contains(lower, "permission prompt") ||
		strings.Contains(lower, "tool use rejected") ||
		strings.Contains(lower, "not allowed to use") ||
		strings.Contains(lower, "operation not permitted")
}

func isClaudeCLIAuthError(lower string) bool {
	return strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "login required") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "401")
}
