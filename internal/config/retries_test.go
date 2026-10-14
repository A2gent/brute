package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLLMRetries(t *testing.T) {
	for _, tc := range []struct {
		name, file, env string
		want            int
	}{
		{"omitted", `{}`, "", 3},
		{"file_zero", `{"llm_retries":0}`, "", 0},
		{"file_positive", `{"llm_retries":2}`, "", 2},
		{"env_zero", `{}`, "0", 0},
		{"env_positive", `{}`, "2", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.file), 0600); err != nil {
				t.Fatal(err)
			}
			isolateLoadFileLookup(t, path)
			t.Setenv("AAGENT_DATA_PATH", t.TempDir())
			t.Setenv("AAGENT_LLM_RETRIES", tc.env)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LLMRetries != tc.want {
				t.Fatalf("LLMRetries = %d, want %d", cfg.LLMRetries, tc.want)
			}
		})
	}
}
