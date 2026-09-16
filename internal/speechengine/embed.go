package speechengine

import (
	_ "embed"
	"fmt"
	"os"
)

//go:embed scripts/local-speech-mlx.py
var embeddedMLXScript []byte

func materializeEmbeddedMLXScript() (path string, cleanup func(), err error) {
	if len(embeddedMLXScript) == 0 {
		return "", nil, fmt.Errorf("embedded mlx helper script is empty")
	}
	tmp, err := os.CreateTemp("", "aagent-local-speech-mlx-*.py")
	if err != nil {
		return "", nil, fmt.Errorf("create embedded mlx helper temp file: %w", err)
	}
	path = tmp.Name()
	cleanup = func() { _ = os.Remove(path) }
	if _, err := tmp.Write(embeddedMLXScript); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write embedded mlx helper: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close embedded mlx helper: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("chmod embedded mlx helper: %w", err)
	}
	return path, cleanup, nil
}
