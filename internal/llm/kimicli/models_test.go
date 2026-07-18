package kimicli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListModelCatalogUsesKimiCLIProviderList(t *testing.T) {
	tmp := t.TempDir()
	fakeKimi := filepath.Join(tmp, "kimi")
	script := "#!/bin/sh\n" +
		"test \"$1\" = \"provider\" || exit 21\n" +
		"test \"$2\" = \"list\" || exit 22\n" +
		"test \"$3\" = \"--json\" || exit 23\n" +
		"printf '%s\\n' '{\"models\":{\"kimi-code/k3\":{\"model\":\"kimi-code/k3\",\"displayName\":\"K3\"},\"kimi-code/kimi-for-coding\":{\"model\":\"kimi-code/kimi-for-coding\",\"displayName\":\"Kimi for Coding\"}}}'\n"
	if err := os.WriteFile(fakeKimi, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake Kimi CLI: %v", err)
	}

	t.Setenv("AAGENT_KIMI_CLI_PATH", fakeKimi)

	got := ListModelCatalog(context.Background(), tmp)
	want := []string{"kimi-code/k3", "kimi-code/kimi-for-coding"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListModelCatalog() = %v, want %v", got, want)
	}
}

func TestListModelCatalogFallsBackWhenKimiCLIQueryFails(t *testing.T) {
	tmp := t.TempDir()
	fakeKimi := filepath.Join(tmp, "kimi")
	if err := os.WriteFile(fakeKimi, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake Kimi CLI: %v", err)
	}

	t.Setenv("AAGENT_KIMI_CLI_PATH", fakeKimi)

	got := ListModelCatalog(context.Background(), tmp)
	want := fallbackModels()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListModelCatalog() = %v, want %v", got, want)
	}
}
