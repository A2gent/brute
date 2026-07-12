package runtimeenv

import (
	"os"
	"strings"
)

// MergeCustomEnv overlays persisted custom env onto the current process env.
// Existing process/container variables win so Docker, shell, and CI overrides
// keep their documented precedence.
func MergeCustomEnv(customEnv map[string]string) {
	for key, value := range customEnv {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if existing := strings.TrimSpace(os.Getenv(k)); existing != "" {
			continue
		}
		_ = os.Setenv(k, value)
	}
}

// SyncCustomEnv updates only variables managed through custom_env. It does not
// touch app settings and it preserves explicit process/container env values.
func SyncCustomEnv(previous map[string]string, next map[string]string) map[string]string {
	applied := make(map[string]string)
	for key, value := range next {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if existing := strings.TrimSpace(os.Getenv(k)); existing != "" {
			continue
		}
		_ = os.Setenv(k, value)
		applied[k] = value
	}
	for key := range previous {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if _, ok := next[k]; ok {
			continue
		}
		if applied[k] != "" {
			continue
		}
		_ = os.Unsetenv(k)
	}
	return applied
}
