package cursorcli

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

var (
	ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	modelIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

var fallbackModels = []string{
	"composer-2.5",
	"composer-latest",
	"auto",
}

// ListModels asks Cursor Agent CLI for the models available to the configured account.
func ListModels(ctx context.Context, options Options) ([]string, error) {
	client := NewClientWithOptions("", options)
	agentPath, err := findExecutable(client.options.Executable)
	if err != nil {
		return nil, fmt.Errorf("Cursor Agent CLI executable %q was not found: %w", client.options.Executable, err)
	}

	cmd := exec.CommandContext(ctx, agentPath, "--list-models")
	cmd.Dir = client.options.WorkDir
	cmd.Env = client.commandEnv()

	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = defaultMaxOutputBytes
	stderr.limit = defaultMaxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("Cursor Agent CLI failed to list models: %s", normalizeCursorCLIErrorMessage(cliErrorMessage(err, stdout.String(), stderr.String())))
	}

	models := parseModelListOutput(stdout.String())
	if len(models) == 0 {
		return nil, fmt.Errorf("Cursor Agent CLI returned no models")
	}
	return models, nil
}

// ListModelCatalog returns account-specific models when discovery succeeds and
// keeps older or unauthenticated CLI installations usable through known aliases.
func ListModelCatalog(ctx context.Context, options Options) []string {
	models, err := ListModels(ctx, options)
	if err == nil && len(models) > 0 {
		return models
	}
	logging.Warn("Cursor model discovery failed, using fallback catalog: %v", err)
	return append([]string(nil), fallbackModels...)
}

func parseModelListOutput(raw string) []string {
	raw = ansiEscapePattern.ReplaceAllString(raw, "")
	models := make([]string, 0)
	seen := make(map[string]struct{})

	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		modelID, _, ok := strings.Cut(strings.TrimSpace(line), " - ")
		modelID = strings.TrimSpace(modelID)
		if !ok || !modelIDPattern.MatchString(modelID) {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		models = append(models, modelID)
	}
	return models
}
