package claudecli

import (
	"reflect"
	"testing"
)

func TestParseHelpOutputExtractsModelAliasesAndCommands(t *testing.T) {
	t.Parallel()

	sample := `Usage: claude [options] [command]

Options:
  --model <model>                       Model for the current session. Provide
                                        an alias for the latest model (e.g.
                                        'fable', 'opus', or 'sonnet') or a
                                        model's full name (e.g.
                                        'claude-fable-5').
  --help                                Show help

Commands:
  agents [options]                      Manage background agents
  auth                                  Manage authentication
  doctor                                Check the health of your installation.
                                        Reads settings files without inference.
  plugin|plugins                        Manage Claude Code plugins
  update|upgrade                        Check for updates
`
	models, commands := parseHelpOutput(sample)
	wantModels := []string{"fable", "opus", "sonnet", "claude-fable-5"}
	wantCommands := []string{"agents", "auth", "doctor", "plugin", "plugins", "update", "upgrade"}
	if !reflect.DeepEqual(models, wantModels) {
		t.Fatalf("models = %v, want %v", models, wantModels)
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
}
