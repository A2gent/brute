package speechengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunExternalFailureDetails(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, want string
	}{
		{"structured error beats progress", `{"error":"no speech detected"}`, "Fetching 4 files: 100%", "no speech detected"},
		{"structured error without stderr", `{"error":"model unavailable"}`, "", "model unavailable"},
		{"trimmed structured error", `{"error":"  no speech detected  "}`, "progress", "no speech detected"},
		{"non-string error fallback", `{"error":1}`, "model load failed", "model load failed"},
		{"unrelated json fallback", `{"text":""}`, "model load failed", "model load failed"},
		{"stderr fallback", "", "Traceback: model load failed", "Traceback: model load failed"},
		{"stdout fallback", "model load failed", "", "model load failed"},
		{"malformed json fallback", "{invalid", "model load failed", "model load failed"},
		{"empty json error fallback", `{"error":"  "}`, "model load failed", "model load failed"},
		{"exit status fallback", "", "", "exit status 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "python")
			if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"$1\"\nprintf '%s' \"$2\" >&2\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			result, err := runExternal(context.Background(), path, tc.stdout, tc.stderr)
			if err == nil || err.Error() != "python failed: "+tc.want {
				t.Fatalf("error = %v, want python failed: %s", err, tc.want)
			}
			if string(result.stdout) != tc.stdout || string(result.stderr) != tc.stderr {
				t.Fatal("raw process output was not preserved")
			}
		})
	}
}
