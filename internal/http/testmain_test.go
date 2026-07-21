package http

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	testDir, err := os.MkdirTemp("", "aagent-http-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated HTTP test config directory: %v\n", err)
		os.Exit(1)
	}

	// Several HTTP handlers persist configuration. Isolate the entire package
	// so a new handler test can never fall through to the user's real config.
	if err := os.Setenv("AAGENT_CONFIG_PATH", filepath.Join(testDir, "config.json")); err != nil {
		fmt.Fprintf(os.Stderr, "set isolated HTTP test config path: %v\n", err)
		_ = os.RemoveAll(testDir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(testDir)
	os.Exit(code)
}
