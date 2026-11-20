package antigravitycli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

const DefaultModel = "gemini-4-hydrogen-medium"

var fallbackModels = []string{
	"gemini-4-hydrogen-high",
	"gemini-4-hydrogen-medium",
	"gemini-4-hydrogen-low",
	"gemini-4-neon-high",
	"gemini-4-neon-medium",
	"gemini-4-neon-low",
	"gemini-4-argon-high",
	"gemini-4-argon-medium",
	"gemini-4-argon-low",
	"gemini-3.8-flash-high",
	"gemini-3.8-flash-medium",
	"gemini-3.8-flash-low",
}

func NormalizeModel(raw string) string {
	model := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "google/"))
	if model == "" {
		return DefaultModel
	}
	for _, known := range fallbackModels {
		if known == model {
			return model
		}
	}
	for _, known := range fallbackModels {
		if known == model+"-medium" {
			return known
		}
	}
	return model
}

func ListModels(ctx context.Context, executable string) ([]string, error) {
	agyPath, err := ResolveExecutable(executable)
	if err != nil {
		return nil, fmt.Errorf("Antigravity CLI executable was not found: %w", err)
	}
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = defaultMaxOutputBytes, defaultMaxOutputBytes
	cmd := exec.CommandContext(ctx, agyPath, "models")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Antigravity CLI failed to list models: %s", normalizeErrorMessage(firstNonEmpty(stderr.String(), stdout.String(), err.Error())))
	}
	models := parseModelList(stdout.String())
	if len(models) == 0 {
		return nil, fmt.Errorf("Antigravity CLI returned no models")
	}
	return models, nil
}

func ListModelCatalog(ctx context.Context, executable string) []string {
	models, err := ListModels(ctx, executable)
	if err == nil {
		return models
	}
	logging.Warn("Antigravity model discovery failed, using fallback catalog: %v", err)
	return append([]string(nil), fallbackModels...)
}

func parseModelList(raw string) []string {
	models := make([]string, 0)
	seen := map[string]struct{}{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		id, _, ok := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		if !ok || id == "" || strings.Contains(id, " ") {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	return models
}
