package speechengine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPronunciationOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AAGENT_DATA_PATH", dir)

	if got := normalizeRussianText("kubernetes"); got == "куб+ернетис" {
		t.Fatalf("precondition: override must not exist yet, got %q", got)
	}

	// Cache fills in unknown words and survives reload.
	if err := putCachedPronunciation("Kubernetes", "куберн+етис"); err != nil {
		t.Fatal(err)
	}
	if got := normalizeRussianText("kubernetes"); got != "куберн+етис" {
		t.Errorf("cache: got %q", got)
	}

	// User file beats cache and built-in dictionary (case-insensitive), and applies to Russian words.
	user := `{"Kubernetes": "куберн+етес", "suggest": "сэдж+ест", "кубернетес": "куберн+етес"}`
	if err := os.WriteFile(filepath.Join(dir, "speech", userPronunciationsFile), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := normalizeRussianText("Kubernetes и кубернетес"); got != "куберн+етес и куберн+етес" {
		t.Errorf("user override: got %q", got)
	}

	// Malformed user file is ignored instead of breaking synthesis.
	if err := os.WriteFile(filepath.Join(dir, "speech", userPronunciationsFile), []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := normalizeRussianText("kubernetes"); got != "куберн+етис" {
		t.Errorf("malformed user file: got %q", got)
	}
}
