package http

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// testBlenderIntegration verifies the configured Blender binary is executable.
// WHY: Blender exposes no HTTP API (unlike ComfyUI), so connectivity is proven
// by running `blender --version` on the host instead of probing an endpoint.
func (s *Server) testBlenderIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	binary, err := resolveBlenderBinary(integration.Config["binary_path"])
	if err != nil {
		return false, err.Error()
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return false, fmt.Sprintf("Blender at %s could not be started: %s", binary, detail)
	}

	version := strings.TrimSpace(string(output))
	if idx := strings.IndexByte(version, '\n'); idx >= 0 {
		version = strings.TrimSpace(version[:idx])
	}
	if version == "" {
		version = "unknown version"
	}
	return true, fmt.Sprintf("Connected to %s (%s)", version, binary)
}

// resolveBlenderBinary falls back to PATH lookup when no explicit path is set.
func resolveBlenderBinary(configured string) (string, error) {
	binary := strings.TrimSpace(configured)
	if binary == "" {
		binary = "blender"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("Blender binary %q is not executable: %v", binary, err)
	}
	return resolved, nil
}
