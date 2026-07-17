package kimicli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func normalizeExecutable(raw string) string {
	if path := strings.TrimSpace(os.Getenv("AAGENT_KIMI_CLI_PATH")); path != "" {
		return path
	}
	if raw = strings.TrimSpace(raw); raw != "" {
		return raw
	}
	return defaultExecutable
}

func findExecutable(raw string) (string, error) {
	executable := normalizeExecutable(raw)
	if strings.Contains(executable, string(os.PathSeparator)) {
		if isExecutableFile(executable) {
			return executable, nil
		}
		return "", os.ErrNotExist
	}
	if path, err := exec.LookPath(executable); err == nil {
		return path, nil
	}
	for _, candidate := range commonExecutablePaths(executable) {
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func commonExecutablePaths(executable string) []string {
	paths := []string{
		filepath.Join("/usr/local/bin", executable),
		filepath.Join("/opt/homebrew/bin", executable),
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append([]string{
			filepath.Join(home, ".kimi-code", "bin", executable),
			filepath.Join(home, ".local", "bin", executable),
		}, paths...)
	}
	return paths
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func normalizeWorkDir(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "."
	}
	if abs, err := filepath.Abs(raw); err == nil {
		return abs
	}
	return raw
}

func normalizeModel(raw string) string {
	return strings.TrimSpace(raw)
}

func isKimiSessionID(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "session_") && len(raw) > len("session_")
}

func envBoolDefault(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
