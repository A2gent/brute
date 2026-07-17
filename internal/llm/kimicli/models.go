package kimicli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	defaultExecutable = "kimi"
	defaultModel      = "kimi-code/kimi-for-coding"
)

type providerListJSON struct {
	Models map[string]providerListModel `json:"models"`
}

type providerListModel struct {
	Model       string `json:"model"`
	DisplayName string `json:"displayName"`
}

// ListModelCatalog returns configured Kimi Code CLI model aliases from
// `kimi provider list --json`, falling back to a curated list when the CLI is
// unavailable or not yet configured.
func ListModelCatalog(ctx context.Context, workDir string) []string {
	if _, err := findExecutable(""); err != nil {
		return fallbackModels()
	}

	kimiPath, err := findExecutable("")
	if err != nil {
		return fallbackModels()
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, kimiPath, "provider", "list", "--json")
	cmd.Dir = normalizeWorkDir(workDir)
	output, err := cmd.Output()
	if err != nil {
		return fallbackModels()
	}

	var payload providerListJSON
	if err := json.Unmarshal(output, &payload); err != nil {
		return fallbackModels()
	}

	models := make([]string, 0, len(payload.Models))
	for alias := range payload.Models {
		alias = strings.TrimSpace(alias)
		if alias != "" {
			models = append(models, alias)
		}
	}
	sort.Strings(models)
	if len(models) == 0 {
		return fallbackModels()
	}
	return models
}

func fallbackModels() []string {
	return []string{
		"kimi-code/kimi-for-coding",
		"kimi-code/kimi-for-coding-highspeed",
		"kimi-code/k3",
	}
}

func defaultModelFromConfig() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return defaultModel
	}
	data, err := os.ReadFile(strings.TrimSpace(home) + "/.kimi-code/config.toml")
	if err != nil {
		return defaultModel
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "default_model") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		if value != "" {
			return value
		}
	}
	return defaultModel
}
