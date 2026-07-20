package cursorauth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromAuthFile(t *testing.T) {
	t.Setenv("AAGENT_CURSOR_ACCESS_TOKEN", "")
	t.Setenv("AAGENT_CURSOR_SKIP_PLATFORM_AUTH", "true")

	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"accessToken":"file-access-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	oauth, resolvedPath, err := Load(authPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if oauth.AccessToken != "file-access-token" {
		t.Fatalf("token = %q", oauth.AccessToken)
	}
	if resolvedPath != authPath {
		t.Fatalf("resolved path = %q", resolvedPath)
	}
}

func TestLoadPrefersEnvOverrideWhenNoExplicitPath(t *testing.T) {
	t.Setenv("AAGENT_CURSOR_ACCESS_TOKEN", "env-access-token")
	t.Setenv("AAGENT_CURSOR_SKIP_PLATFORM_AUTH", "true")

	oauth, source, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if oauth.AccessToken != "env-access-token" {
		t.Fatalf("token = %q", oauth.AccessToken)
	}
	if source != cursorAccessTokenEnv {
		t.Fatalf("source = %q", source)
	}
}
