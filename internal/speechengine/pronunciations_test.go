package speechengine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPronunciationOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AAGENT_DATA_PATH", dir)

	if got := normalizeRussianText("kubernetes"); got != "кубернет+ис" {
		t.Fatalf("built-in pronunciation should be stable, got %q", got)
	}

	// Cache fills in unknown words and survives reload, but not at the expense of built-ins.
	if err := putCachedPronunciation("Zorblax", "зорбл+акс"); err != nil {
		t.Fatal(err)
	}
	if got := normalizeRussianText("zorblax"); got != "зорбл+акс" {
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
	if got := normalizeRussianText("kubernetes"); got != "кубернет+ис" {
		t.Errorf("malformed user file: got %q", got)
	}
}
