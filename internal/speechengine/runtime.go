package speechengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/stt/whispercpp"
)

// mlxProbeScript is executed by python -c. JSON true/false are NameErrors here; use Python True/False.
const mlxProbeScript = `import json, sys
from importlib.metadata import version, PackageNotFoundError
if sys.version_info < (3, 10):
    print(json.dumps({"ok": False, "detail": "Python 3.10+ required"}))
    raise SystemExit(1)
try:
    import mlx_audio  # noqa: F401
    from mlx_audio.stt.utils import load as _stt_load  # noqa: F401
    from mlx_audio.tts.utils import load_model as _tts_load  # noqa: F401
    pkg_version = version("mlx-audio")
    parts = [int(p) for p in pkg_version.split(".")[:3]]
    while len(parts) < 3:
        parts.append(0)
    if tuple(parts) < (0, 5, 4):
        print(json.dumps({"ok": False, "detail": f"mlx-audio {pkg_version} is too old; need >= 0.5.4"}))
        raise SystemExit(1)
except PackageNotFoundError:
    print(json.dumps({"ok": False, "detail": "mlx-audio package not found"}))
    raise SystemExit(1)
except Exception as exc:
    print(json.dumps({"ok": False, "detail": str(exc)}))
    raise SystemExit(1)
print(json.dumps({"ok": True}))
`

// InspectRuntime probes local speech dependencies and engines without downloading models.
func InspectRuntime(ctx context.Context) RuntimeStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, defaultInspectLimit*time.Second)
	defer cancel()

	p := currentPlatform()
	status := RuntimeStatus{
		OS:   p.goos,
		Arch: p.goarch,
	}
	if status.OS == "" {
		status.OS = runtime.GOOS
	}
	if status.Arch == "" {
		status.Arch = runtime.GOARCH
	}

	pythonRes := resolvePythonForPlatform(ctx, p)
	mlxProbe := probeMLXRuntimeWithPython(ctx, p, pythonRes)
	status.Dependencies = inspectDependencies(ctx, p, pythonRes, mlxProbe)
	status.Engines = inspectEngines(p, mlxProbe, status.Dependencies)
	return status
}

type mlxProbeResult struct {
	ready  bool
	path   string
	detail string
}

func inspectDependencies(ctx context.Context, p *platformEnv, pythonRes pythonPathResult, mlxProbe mlxProbeResult) []DependencyStatus {
	return []DependencyStatus{
		inspectPythonDependency(p, pythonRes),
		inspectFFmpegDependency(p),
		inspectWhisperKitDependency(p),
		inspectMLXDependency(ctx, p, pythonRes, mlxProbe),
	}
}

func inspectPythonDependency(p *platformEnv, res pythonPathResult) DependencyStatus {
	dep := DependencyStatus{
		ID:        ComponentPython,
		Label:     "Python 3.10+",
		Supported: isDarwinARM64(p),
	}
	if !dep.Supported {
		dep.Detail = "Python bootstrap is supported on macOS Apple Silicon"
		return dep
	}
	if res.OverrideInvalid {
		dep.Detail = "AAGENT_SPEECH_PYTHON is set but not an executable file"
		dep.Installable = false
		return dep
	}
	if res.OverrideVersionLow {
		dep.Path = res.Path
		dep.Detail = "AAGENT_SPEECH_PYTHON is set but Python version is below 3.10"
		dep.Installable = false
		return dep
	}
	dep.Path = res.Path
	dep.Installed = res.Path != ""
	if dep.Installed {
		if res.ExplicitOverride {
			dep.Detail = "Python 3.10+ available via AAGENT_SPEECH_PYTHON override"
		} else {
			dep.Detail = "Python 3.10+ available"
		}
	} else {
		dep.Detail = pythonMissingDetail(p)
	}
	dep.Installable = dep.Supported && homebrewSupported(p) && !dep.Installed
	if !dep.Installed && !dep.Installable {
		dep.Detail = pythonMissingDetail(p)
	}
	return dep
}

func pythonMissingDetail(p *platformEnv) string {
	if !homebrewSupported(p) {
		return "Install Homebrew, then run: brew install python@3.11 (or provide Python 3.10+ on PATH)"
	}
	return "Install with Homebrew: brew install python@3.11 (or provide Python 3.10+ on PATH)"
}

func inspectFFmpegDependency(p *platformEnv) DependencyStatus {
	dep := DependencyStatus{
		ID:        ComponentFFmpeg,
		Label:     "FFmpeg",
		Supported: isDarwin(p),
	}
	if !dep.Supported {
		dep.Detail = "Homebrew FFmpeg install is supported on macOS"
		return dep
	}
	dep.Path = resolveFFmpegForPlatform(p)
	dep.Installed = dep.Path != ""
	if dep.Installed {
		dep.Detail = "ffmpeg found"
	} else {
		dep.Detail = ffmpegMissingDetail(p)
	}
	dep.Installable = dep.Supported && homebrewSupported(p) && !dep.Installed
	if !dep.Installed && !dep.Installable && dep.Supported {
		dep.Detail = ffmpegMissingDetail(p)
	}
	return dep
}

func ffmpegMissingDetail(p *platformEnv) string {
	if !homebrewSupported(p) {
		return "Install Homebrew, then run: brew install ffmpeg"
	}
	return "Install with Homebrew: brew install ffmpeg"
}

func inspectWhisperKitDependency(p *platformEnv) DependencyStatus {
	dep := DependencyStatus{
		ID:        ComponentWhisperKit,
		Label:     "WhisperKit CLI",
		Supported: isDarwin(p),
	}
	if !dep.Supported {
		dep.Detail = "WhisperKit is supported on macOS"
		return dep
	}
	dep.Path = resolveWhisperKitBinForPlatform(p)
	dep.Installed = dep.Path != ""
	if dep.Installed {
		dep.Detail = "whisperkit-cli found"
	} else {
		dep.Detail = whisperKitMissingDetail(p)
	}
	dep.Installable = dep.Supported && homebrewSupported(p) && !dep.Installed
	if !dep.Installed && !dep.Installable && dep.Supported {
		dep.Detail = whisperKitMissingDetail(p)
	}
	return dep
}

func whisperKitMissingDetail(p *platformEnv) string {
	if !homebrewSupported(p) {
		return "Install Homebrew, then run: brew install whisperkit-cli"
	}
	return "Install with Homebrew: brew install whisperkit-cli"
}

func inspectMLXDependency(ctx context.Context, p *platformEnv, pythonRes pythonPathResult, probe mlxProbeResult) DependencyStatus {
	dep := DependencyStatus{
		ID:        ComponentMLX,
		Label:     "mlx-audio runtime",
		Supported: isDarwinARM64(p),
		Path:      managedMLXVenvDir(p),
	}
	if !dep.Supported {
		dep.Detail = "mlx-audio managed runtime is supported on macOS Apple Silicon"
		return dep
	}
	dep.Installed = probe.ready
	dep.Path = probe.path
	if probe.ready {
		dep.Detail = "mlx-audio imports verified (model weights not checked)"
	} else if probe.detail != "" {
		dep.Detail = probe.detail
	} else if managedMLXPython(p) != "" {
		dep.Detail = "Managed venv exists but mlx-audio is not importable"
	} else {
		dep.Detail = "Managed venv not installed at " + managedMLXVenvDir(p)
	}
	bootstrap := ""
	if pythonRes.Path != "" && !pythonRes.ExplicitOverride {
		bootstrap = pythonRes.Path
	} else {
		bootstrap = discoverBootstrapPython(ctx, p)
	}
	dep.Installable = dep.Supported && bootstrap != "" && !dep.Installed
	if !dep.Installed && !dep.Installable && dep.Supported {
		if bootstrap == "" {
			dep.Detail = mlxMissingBootstrapDetail(p)
		}
	}
	if explicitPythonOverrideSet() && managedMLXPython(p) != "" {
		dep.Detail += "; explicit Python override is active. To use the managed installation, clear AAGENT_SPEECH_PYTHON and save"
	}
	return dep
}

func mlxMissingBootstrapDetail(p *platformEnv) string {
	if !homebrewSupported(p) {
		return "Install Homebrew and Python 3.10+ (prefer 3.11), or place python3.11/python3 on PATH"
	}
	return "Install Python 3.10+ bootstrap (prefer brew install python@3.11) before installing mlx-audio runtime"
}

func resolveWhisperKitBinForPlatform(p *platformEnv) string {
	for _, key := range []string{"AAGENT_SPEECH_WHISPERKIT_BIN", "AAGENT_WHISPERKIT_BIN"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			path := filepath.Clean(raw)
			if pathIsExecutable(path) {
				return path
			}
			return ""
		}
	}
	for _, candidate := range []string{"whisperkit-cli", "argmax-cli"} {
		if path, err := p.lookPath(candidate); err == nil && pathIsExecutable(path) {
			return path
		}
	}
	for _, dir := range brewBinDirsForPlatform(p) {
		for _, name := range []string{"whisperkit-cli", "argmax-cli"} {
			candidate := filepath.Join(dir, name)
			if pathIsExecutable(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func probeMLXRuntime(ctx context.Context, p *platformEnv) mlxProbeResult {
	return probeMLXRuntimeWithPython(ctx, p, resolvePythonForPlatform(ctx, p))
}

func probeMLXRuntimeWithPython(ctx context.Context, p *platformEnv, res pythonPathResult) mlxProbeResult {
	if !isDarwinARM64(p) {
		return mlxProbeResult{detail: "mlx-audio runtime requires macOS Apple Silicon"}
	}
	if res.OverrideInvalid {
		return mlxProbeResult{detail: "AAGENT_SPEECH_PYTHON is set but not an executable file"}
	}
	if res.OverrideVersionLow {
		return mlxProbeResult{path: res.Path, detail: "AAGENT_SPEECH_PYTHON is set but Python version is below 3.10"}
	}
	python := res.Path
	if python == "" {
		return mlxProbeResult{detail: "No Python runtime configured for mlx-audio probe"}
	}
	stdout, stderr, err := p.runner.Run(ctx, python, "-c", mlxProbeScript)
	if err != nil {
		detail := strings.TrimSpace(string(stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(stdout))
		}
		if detail == "" {
			detail = err.Error()
		}
		return mlxProbeResult{path: python, detail: detail}
	}
	var payload struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	if jsonErr := json.Unmarshal(stdout, &payload); jsonErr != nil {
		return mlxProbeResult{path: python, detail: "invalid mlx probe response"}
	}
	if !payload.OK {
		detail := strings.TrimSpace(payload.Detail)
		if detail == "" {
			detail = "mlx-audio import probe failed"
		}
		return mlxProbeResult{path: python, detail: detail}
	}
	return mlxProbeResult{ready: true, path: python, detail: "mlx-audio imports verified"}
}

func inspectEngines(p *platformEnv, mlxProbe mlxProbeResult, deps []DependencyStatus) []EngineStatus {
	depReady := func(id string) bool {
		for _, dep := range deps {
			if dep.ID == id {
				return dep.Installed
			}
		}
		return false
	}

	cfg := loadRuntimeConfigModels("")
	engines := []EngineStatus{
		mlxEngineStatus(p, EngineParakeet, "Parakeet (NVIDIA)", "stt", cfg.parakeetModel, ComponentMLX, mlxProbe.ready),
		mlxEngineStatus(p, EngineMoonshine, "Moonshine", "stt", cfg.moonshineModel, ComponentMLX, mlxProbe.ready),
		mlxEngineStatus(p, EngineKokoro, "Kokoro v1.0", "tts", cfg.kokoroModel, ComponentMLX, mlxProbe.ready),
		mlxEngineStatus(p, EngineQwen3TTS, "Qwen3 TTS", "tts", cfg.qwen3TTSModel, ComponentMLX, mlxProbe.ready),
		{
			ID:           EngineWhisperKit,
			Label:        "WhisperKit (Argmax)",
			Kind:         "stt",
			Model:        cfg.whisperKitModel,
			RuntimeID:    ComponentWhisperKit,
			Supported:    isDarwin(p),
			RuntimeReady: depReady(ComponentWhisperKit),
			Detail:       whisperKitEngineDetail(depReady(ComponentWhisperKit)),
		},
		legacyWhisperCPPEngine(p),
		legacyPiperEngine(p),
		legacyMacOSSayEngine(p),
	}
	return engines
}

func mlxEngineStatus(p *platformEnv, id, label, kind, model, runtimeID string, ready bool) EngineStatus {
	return EngineStatus{
		ID:           id,
		Label:        label,
		Kind:         kind,
		Model:        model,
		RuntimeID:    runtimeID,
		Supported:    isDarwinARM64(p),
		RuntimeReady: ready,
		Detail:       mlxEngineDetail(ready),
	}
}

func mlxEngineDetail(ready bool) string {
	if ready {
		return "mlx-audio runtime ready (model weights not verified)"
	}
	return "mlx-audio runtime not ready"
}

func whisperKitEngineDetail(ready bool) string {
	if ready {
		return "WhisperKit CLI available (model weights not verified)"
	}
	return "WhisperKit CLI not found"
}

func legacyWhisperCPPEngine(p *platformEnv) EngineStatus {
	eng := EngineStatus{
		ID:        EngineWhisperCPP,
		Label:     "whisper.cpp",
		Kind:      "stt",
		RuntimeID: EngineWhisperCPP,
		Supported: true,
		Detail:    "Legacy engine; auto-setup and model weights are not fully probed here",
	}
	path, source := resolveWhisperCPPBinForPlatform(p)
	if path == "" {
		return eng
	}
	eng.RuntimeReady = true
	switch source {
	case "env":
		eng.Detail = "Configured via AAGENT_WHISPER_BIN at " + path
	case "managed":
		eng.Detail = "Managed whisper-cli found at " + path
	default:
		eng.Detail = "whisper-cli found at " + path
	}
	return eng
}

func resolveWhisperCPPBin() string {
	path, _ := resolveWhisperCPPBinForPlatform(currentPlatform())
	return path
}

// Same order as whispercpp.resolveBinaryPath, plus Homebrew dirs when PATH is incomplete.
func resolveWhisperCPPBinForPlatform(p *platformEnv) (path string, source string) {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_WHISPER_BIN")); raw != "" {
		cleaned := filepath.Clean(raw)
		if pathExists(cleaned) {
			return cleaned, "env"
		}
		return "", ""
	}
	if found, err := p.lookPath("whisper-cli"); err == nil && pathIsExecutable(found) {
		return found, "path"
	}
	for _, dir := range brewBinDirsForPlatform(p) {
		candidate := filepath.Join(dir, "whisper-cli")
		if pathIsExecutable(candidate) {
			return candidate, "brew"
		}
	}
	dataDir := strings.TrimSpace(p.dataPath)
	if dataDir == "" {
		dataDir = resolveSpeechDataPath()
	}
	for _, candidate := range whispercpp.ManagedBinaryCandidates(dataDir) {
		if pathIsExecutable(candidate) || pathExists(candidate) {
			return candidate, "managed"
		}
	}
	return "", ""
}

func legacyPiperEngine(p *platformEnv) EngineStatus {
	eng := EngineStatus{
		ID:        "piper_tts",
		Label:     "Piper TTS",
		Kind:      "tts",
		RuntimeID: "piper_tts",
		Supported: true,
		Detail:    "Legacy engine; Piper auto-setup and models are not fully probed here",
	}
	if raw := strings.TrimSpace(os.Getenv("PIPER_BIN")); raw != "" && pathExists(raw) {
		eng.RuntimeReady = true
		eng.Detail = "Configured via PIPER_BIN at " + raw
		return eng
	}
	if path, err := p.lookPath("piper"); err == nil {
		eng.RuntimeReady = true
		eng.Detail = "piper found at " + path
	}
	return eng
}

func legacyMacOSSayEngine(p *platformEnv) EngineStatus {
	eng := EngineStatus{
		ID:        "macos_say_tts",
		Label:     "macOS Say",
		Kind:      "tts",
		RuntimeID: "macos_say_tts",
		Supported: isDarwin(p),
		Detail:    "Legacy engine; uses macOS say(1) when available",
	}
	if !eng.Supported {
		eng.Detail = "macOS Say is only available on macOS"
		return eng
	}
	if path, err := p.lookPath("say"); err == nil {
		eng.RuntimeReady = true
		eng.Detail = "say found at " + path
	}
	return eng
}
