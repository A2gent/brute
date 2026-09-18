package speechengine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type pythonPathResult struct {
	Path               string
	ExplicitOverride   bool
	OverrideInvalid    bool
	OverrideVersionLow bool
}

func resolvePythonForPlatform(ctx context.Context, p *platformEnv) pythonPathResult {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_SPEECH_PYTHON")); raw != "" {
		path := filepath.Clean(raw)
		if !pathIsExecutable(path) {
			return pythonPathResult{OverrideInvalid: true}
		}
		if !pythonMeetsMinimum(ctx, p, path) {
			return pythonPathResult{Path: path, ExplicitOverride: true, OverrideVersionLow: true}
		}
		return pythonPathResult{Path: path, ExplicitOverride: true}
	}
	if managed := managedMLXPython(p); managed != "" && pathIsExecutable(managed) {
		return pythonPathResult{Path: managed}
	}
	if !isDarwinARM64(p) {
		return pythonPathResult{}
	}
	if path := discoverBootstrapPython(ctx, p); path != "" {
		return pythonPathResult{Path: path}
	}
	return pythonPathResult{}
}

func resolvePythonPath() string {
	ctx, cancel := context.WithTimeout(context.Background(), pythonProbeTimeout*time.Second)
	defer cancel()
	return resolvePythonForPlatform(ctx, currentPlatform()).Path
}

func explicitPythonOverrideSet() bool {
	return strings.TrimSpace(os.Getenv("AAGENT_SPEECH_PYTHON")) != ""
}

func discoverBootstrapPython(ctx context.Context, p *platformEnv) string {
	candidates := make([]string, 0, 8)
	for _, dir := range brewBinDirsForPlatform(p) {
		candidates = append(candidates, filepath.Join(dir, "python3.11"))
	}
	if path, err := p.lookPath("python3.11"); err == nil {
		candidates = append(candidates, path)
	}
	for _, candidate := range candidates {
		if pathIsExecutable(candidate) && pythonMeetsMinimum(ctx, p, candidate) {
			return candidate
		}
	}
	for _, name := range []string{"python3", "python"} {
		if resolved, err := p.lookPath(name); err == nil && pathIsExecutable(resolved) && pythonMeetsMinimum(ctx, p, resolved) {
			return resolved
		}
	}
	return ""
}

func pathIsExecutable(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(path)
		return strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".bat") || strings.HasSuffix(lower, ".cmd")
	}
	return info.Mode()&0o111 != 0
}
