package http

import (
	"fmt"
	"strings"
)

const (
	sessionQueueModeAuto = "auto"
	sessionQueueModeNow  = "now"
)

func normalizeSessionQueueMode(raw string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(raw))
	if normalized == "" {
		return "", nil
	}
	switch normalized {
	case sessionQueueModeSerial, sessionQueueModeAuto, sessionQueueModeNow:
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid queue_mode: %s", raw)
	}
}

func applyLeadingSessionQueueDirective(req *CreateSessionRequest) {
	if req == nil {
		return
	}
	prompt, mode, ok := leadingSessionQueueDirective(req.Task)
	if !ok {
		return
	}
	req.Task = prompt
	req.QueueMode = mode
	req.Queued = mode == sessionQueueModeSerial
}

func stripLeadingSessionQueueDirective(raw string) (string, bool) {
	prompt, mode, ok := leadingSessionQueueDirective(raw)
	return prompt, ok && mode == sessionQueueModeSerial
}

func leadingSessionQueueDirective(raw string) (string, string, bool) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"/queue", "/q", "--queue", "-q", "/now"} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		if len(trimmed) > len(prefix) {
			next := trimmed[len(prefix)]
			if next != ':' && next != ' ' && next != '\t' && next != '\r' && next != '\n' {
				continue
			}
		}
		prompt := strings.TrimLeft(trimmed[len(prefix):], " \t\r\n")
		prompt = strings.TrimPrefix(prompt, ":")
		prompt = strings.TrimLeft(prompt, " \t\r\n")
		if strings.TrimSpace(prompt) == "" {
			return "", "", false
		}
		if strings.HasPrefix(prefix, "/") && isQueueModeSelectionOnly(prompt) {
			return "", "", false
		}
		mode := sessionQueueModeSerial
		if prefix == "/now" {
			mode = sessionQueueModeNow
		}
		return prompt, mode, true
	}
	return "", "", false
}

func isQueueModeSelectionOnly(raw string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(raw))), " ")
	switch normalized {
	case "run now", "run_now", "run-now", "now", "start", "immediate", "serial", "queued", "queue":
		return true
	default:
		return false
	}
}
