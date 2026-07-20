package claudecli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/approval"
)

const (
	envSidecarPath       = "AAGENT_CLAUDE_AGENT_SDK_SIDECAR_PATH"
	envSidecarNodePath   = "AAGENT_CLAUDE_AGENT_SDK_NODE_PATH"
	envSidecarApprovalTO = "AAGENT_CLAUDE_AGENT_SDK_APPROVAL_TIMEOUT"
)

// SidecarEnabled reports whether the Claude Agent SDK sidecar transport should be used.
// Requires an explicit non-empty AAGENT_CLAUDE_AGENT_SDK_SIDECAR_PATH and a broker.
func SidecarEnabled(opts Options) bool {
	if opts.Broker == nil {
		return false
	}
	return strings.TrimSpace(os.Getenv(envSidecarPath)) != ""
}

func resolveSidecarPath(opts Options) (string, error) {
	raw := strings.TrimSpace(opts.SidecarPath)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(envSidecarPath))
	}
	if raw == "" {
		return "", fmt.Errorf("sidecar path is not configured")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("sidecar path: %w", err)
	}
	if !isRegularFile(abs) {
		return "", fmt.Errorf("sidecar path %q is not a regular file", abs)
	}
	return abs, nil
}

func resolveNodePath(opts Options) (string, error) {
	raw := strings.TrimSpace(opts.NodePath)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(envSidecarNodePath))
	}
	if raw == "" {
		raw = "node"
	}
	if strings.Contains(raw, string(os.PathSeparator)) {
		if !isExecutableFile(raw) {
			return "", fmt.Errorf("node executable %q is not executable", raw)
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	path, err := findExecutable(raw)
	if err != nil {
		return "", fmt.Errorf("node executable %q was not found in PATH", raw)
	}
	return path, nil
}

func resolveApprovalTimeout(opts Options) time.Duration {
	if opts.ApprovalTimeout > 0 {
		return opts.ApprovalTimeout
	}
	raw := strings.TrimSpace(os.Getenv(envSidecarApprovalTO))
	if raw == "" {
		return approval.DefaultLimits().DefaultTimeout
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return approval.DefaultLimits().DefaultTimeout
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().IsRegular()
}
