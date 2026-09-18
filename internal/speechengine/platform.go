package speechengine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	runtimeLookPath    = exec.LookPath
	getRuntimePlatform = defaultPlatform
)

func currentPlatform() *platformEnv {
	return getRuntimePlatform()
}

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

type execRunner = execStreamingRunner

type platformEnv struct {
	goos     string
	goarch   string
	dataPath string
	brewPath string
	brewDirs []string
	lookPath func(string) (string, error)
	runner   commandRunner
}

func defaultPlatform() *platformEnv {
	return &platformEnv{
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		dataPath: resolveSpeechDataPath(),
		lookPath: runtimeLookPath,
		runner:   execStreamingRunner{},
	}
}

func resolveSpeechDataPath() string {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_DATA_PATH")); raw != "" {
		return filepath.Clean(raw)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Clean(filepath.Join(".", ".aagent-data"))
	}
	return filepath.Join(homeDir, ".local", "share", "aagent")
}

func managedMLXVenvDir(p *platformEnv) string {
	return filepath.Join(p.dataPath, managedMLXVenvRel)
}

func managedVenvValid(p *platformEnv) bool {
	venvDir := managedMLXVenvDir(p)
	if !pathExists(filepath.Join(venvDir, "pyvenv.cfg")) {
		return false
	}
	return managedMLXPythonPath(p) != ""
}

func managedMLXPythonPath(p *platformEnv) string {
	path := filepath.Join(managedMLXVenvDir(p), "bin", "python")
	if pathIsExecutable(path) {
		return path
	}
	if runtime.GOOS == "windows" {
		winPath := filepath.Join(managedMLXVenvDir(p), "Scripts", "python.exe")
		if pathIsExecutable(winPath) {
			return winPath
		}
	}
	return ""
}

func managedMLXPython(p *platformEnv) string {
	if !managedVenvValid(p) {
		return ""
	}
	return managedMLXPythonPath(p)
}

func brewBinDirsForPlatform(p *platformEnv) []string {
	if p != nil && len(p.brewDirs) > 0 {
		return p.brewDirs
	}
	return []string{"/opt/homebrew/bin", "/usr/local/bin"}
}

func resolveBrewPath(p *platformEnv) string {
	if brew := strings.TrimSpace(p.brewPath); brew != "" && pathExists(brew) {
		return brew
	}
	for _, dir := range brewBinDirsForPlatform(p) {
		candidate := filepath.Join(dir, "brew")
		if pathExists(candidate) {
			return candidate
		}
	}
	if path, err := p.lookPath("brew"); err == nil {
		return path
	}
	return ""
}

func pythonMeetsMinimum(ctx context.Context, p *platformEnv, pythonPath string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	stdout, _, err := p.runner.Run(ctx, pythonPath, "-c", "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')")
	if err != nil {
		return false
	}
	major, minor, ok := parsePythonVersion(strings.TrimSpace(string(stdout)))
	if !ok {
		return false
	}
	return major > 3 || (major == 3 && minor >= 10)
}

func parsePythonVersion(raw string) (major, minor int, ok bool) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func atoi(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, os.ErrInvalid
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int(ch-'0')
	}
	return n, nil
}

func isDarwinARM64(p *platformEnv) bool {
	return p.goos == "darwin" && p.goarch == "arm64"
}

func isDarwin(p *platformEnv) bool {
	return p.goos == "darwin"
}

func homebrewSupported(p *platformEnv) bool {
	return isDarwin(p) && resolveBrewPath(p) != ""
}
