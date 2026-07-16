package cursorcli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseModelListOutput(t *testing.T) {
	raw := "Available models\n\n" +
		"auto - Auto (default)\n" +
		"gpt-5.3-codex-low - Codex 5.3 Low\n" +
		"cursor-grok-4.5-high - Cursor Grok 4.5\n" +
		"composer-2.5 - Composer 2.5 (current)\n" +
		"\nTip: use --model <id> to switch.\n"

	got := parseModelListOutput(raw)
	want := []string{"auto", "gpt-5.3-codex-low", "cursor-grok-4.5-high", "composer-2.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseModelListOutput() = %v, want %v", got, want)
	}
}

func TestParseModelListOutputStripsANSIAndDeduplicates(t *testing.T) {
	raw := "\x1b[2mAvailable models\x1b[22m\n" +
		"\x1b[36mauto\x1b[39m \x1b[2m- Auto (default)\x1b[22m\n" +
		"auto - duplicate\n" +
		"not a model line\n" +
		"cursor-grok-4.5-high - Cursor Grok 4.5\n"

	got := parseModelListOutput(raw)
	want := []string{"auto", "cursor-grok-4.5-high"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseModelListOutput() = %v, want %v", got, want)
	}
}

func TestListModelsUsesCursorCLIAndAPIKeyEnvironment(t *testing.T) {
	tmp := t.TempDir()
	fakeAgent := filepath.Join(tmp, "agent")
	script := "#!/bin/sh\n" +
		"test \"$1\" = \"--list-models\" || exit 21\n" +
		"test \"$CURSOR_API_KEY\" = \"cursor-test-token\" || exit 22\n" +
		"printf '%s\\n' 'Available models' '' 'auto - Auto (default)' 'cursor-grok-4.5-high - Cursor Grok 4.5' 'composer-2.5 - Composer 2.5 (current)'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake Cursor CLI: %v", err)
	}

	got, err := ListModels(context.Background(), Options{
		Executable: fakeAgent,
		WorkDir:    tmp,
		APIKey:     "cursor-test-token",
	})
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	want := []string{"auto", "cursor-grok-4.5-high", "composer-2.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListModels() = %v, want %v", got, want)
	}
}

func TestListModelCatalogFallsBackWhenCursorCLIQueryFails(t *testing.T) {
	tmp := t.TempDir()
	fakeAgent := filepath.Join(tmp, "agent")
	if err := os.WriteFile(fakeAgent, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake Cursor CLI: %v", err)
	}

	got := ListModelCatalog(context.Background(), Options{Executable: fakeAgent, WorkDir: tmp})
	want := []string{"composer-2.5", "composer-latest", "auto"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListModelCatalog() = %v, want %v", got, want)
	}
}
