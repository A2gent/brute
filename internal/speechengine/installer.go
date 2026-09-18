package speechengine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const installTimeout = 30 * time.Minute

// Installer runs one asynchronous speech-runtime install job at a time.
type Installer struct {
	mu       sync.RWMutex
	job      *InstallJob
	logBuf   strings.Builder
	cancel   context.CancelFunc
	platform *platformEnv
}

// NewInstaller creates a speech runtime installer using the current host environment.
func NewInstaller() *Installer {
	return &Installer{platform: currentPlatform()}
}

// Start begins installing a supported component. Only one job may run at a time.
func (i *Installer) Start(component string) (InstallJob, error) {
	component = strings.ToLower(strings.TrimSpace(component))
	if !isSupportedInstallComponent(component) {
		return InstallJob{}, ErrUnsupportedComponent
	}
	if !componentSupportedOnHost(i.platform, component) {
		return InstallJob{}, ErrUnsupportedPlatform
	}

	i.mu.Lock()
	if i.job != nil && i.job.State == installJobStateRunning {
		i.mu.Unlock()
		return InstallJob{}, ErrInstallBusy
	}

	now := time.Now().UTC().Format(time.RFC3339)
	job := &InstallJob{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		Component: component,
		State:     installJobStateRunning,
		StartedAt: now,
	}
	i.job = job
	i.logBuf.Reset()
	snapshot := i.snapshotJobLocked()
	i.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	i.mu.Lock()
	i.cancel = cancel
	i.mu.Unlock()

	go i.runInstall(ctx, component)
	return snapshot, nil
}

// Job returns the current install job snapshot, or nil when no job has started.
func (i *Installer) Job() *InstallJob {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.job == nil {
		return nil
	}
	job := i.snapshotJobLocked()
	return &job
}

func (i *Installer) snapshotJob() InstallJob {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.snapshotJobLocked()
}

func (i *Installer) snapshotJobLocked() InstallJob {
	job := *i.job
	job.Log = i.logBuf.String()
	return job
}

func (i *Installer) appendLog(line string) {
	line = strings.TrimRight(line, "\n")
	if line == "" {
		return
	}
	i.mu.Lock()
	if i.logBuf.Len() > 0 {
		i.logBuf.WriteByte('\n')
	}
	i.logBuf.WriteString(line)
	if i.logBuf.Len() > maxInstallLogBytes {
		trimmed := i.logBuf.String()
		trimmed = trimmed[len(trimmed)-maxInstallLogBytes:]
		i.logBuf.Reset()
		i.logBuf.WriteString(trimmed)
	}
	i.mu.Unlock()
}

func (i *Installer) finish(state string, err error) {
	i.mu.Lock()
	if i.job != nil {
		i.job.State = state
		i.job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			i.job.Error = err.Error()
		}
	}
	if i.cancel != nil {
		i.cancel()
		i.cancel = nil
	}
	i.mu.Unlock()
}

func (i *Installer) runInstall(ctx context.Context, component string) {
	var err error
	switch component {
	case ComponentPython:
		err = i.installHomebrewPackage(ctx, "python@3.11")
		if err == nil {
			err = i.verifyPythonBootstrap(ctx)
		}
	case ComponentFFmpeg:
		err = i.installHomebrewPackage(ctx, "ffmpeg")
		if err == nil {
			err = i.verifyFFmpegInstalled()
		}
	case ComponentWhisperKit:
		err = i.installHomebrewPackage(ctx, "whisperkit-cli")
		if err == nil {
			err = i.verifyWhisperKitInstalled()
		}
	case ComponentMLX:
		err = i.installMLXRuntime(ctx)
		if err == nil {
			err = i.verifyMLXRuntime(ctx)
			if err == nil && explicitPythonOverrideSet() {
				i.appendLog("Managed mlx runtime installed; clear AAGENT_SPEECH_PYTHON to activate it")
			}
		}
	default:
		err = ErrUnsupportedComponent
	}
	if err != nil {
		i.appendLog(err.Error())
		i.finish(installJobStateFailed, err)
		return
	}
	i.finish(installJobStateSucceeded, nil)
}

func (i *Installer) installHomebrewPackage(ctx context.Context, formula string) error {
	brew := resolveBrewPath(i.platform)
	if brew == "" {
		return fmt.Errorf("homebrew not found")
	}
	i.appendLog(fmt.Sprintf("running: brew install %s", formula))
	if _, _, err := runCommand(ctx, i.platform, brew, []string{"install", formula}, i.appendLog, nil); err != nil {
		return fmt.Errorf("brew install %s failed: %w", formula, err)
	}
	return nil
}

func (i *Installer) installMLXRuntime(ctx context.Context) error {
	bootstrap := discoverBootstrapPython(ctx, i.platform)
	if bootstrap == "" {
		return fmt.Errorf("Python 3.10+ bootstrap not found; install python component first or provide python3.11/python3 on PATH")
	}
	if !pythonMeetsMinimum(ctx, i.platform, bootstrap) {
		return fmt.Errorf("bootstrap python at %s does not meet Python 3.10+ requirement", bootstrap)
	}

	venvDir := managedMLXVenvDir(i.platform)
	if err := os.MkdirAll(filepath.Dir(venvDir), 0o755); err != nil {
		return fmt.Errorf("create mlx runtime parent dir: %w", err)
	}

	python := managedMLXPython(i.platform)
	if python == "" {
		i.appendLog(fmt.Sprintf("creating managed venv at %s", venvDir))
		if _, _, err := runCommand(ctx, i.platform, bootstrap, []string{"-m", "venv", venvDir}, i.appendLog, nil); err != nil {
			return fmt.Errorf("create venv failed: %w", err)
		}
		python = managedMLXPython(i.platform)
	}
	if !managedVenvValid(i.platform) {
		return fmt.Errorf("managed venv at %s is missing pyvenv.cfg or python binary", venvDir)
	}
	if python == "" {
		return fmt.Errorf("managed venv python not found at %s", venvDir)
	}

	i.appendLog(fmt.Sprintf("installing %s and %s into %s", mlxAudioPackagePin, misakiPackagePin, venvDir))
	if _, _, err := runCommand(ctx, i.platform, python, []string{
		"-m", "pip", "install", "--require-virtualenv",
		mlxAudioPackagePin, misakiPackagePin,
	}, i.appendLog, pipInstallEnv()); err != nil {
		return fmt.Errorf("pip install failed: %w", err)
	}
	return nil
}

func (i *Installer) verifyPythonBootstrap(ctx context.Context) error {
	if discoverBootstrapPython(ctx, i.platform) == "" {
		return fmt.Errorf("python install finished but Python 3.10+ bootstrap is still unavailable")
	}
	return nil
}

func (i *Installer) verifyFFmpegInstalled() error {
	dep := inspectFFmpegDependency(i.platform)
	if !dep.Installed {
		return fmt.Errorf("ffmpeg install finished but ffmpeg is still unavailable")
	}
	return nil
}

func (i *Installer) verifyWhisperKitInstalled() error {
	if resolveWhisperKitBinForPlatform(i.platform) == "" {
		return fmt.Errorf("whisperkit install finished but whisperkit-cli is still unavailable")
	}
	return nil
}

func (i *Installer) verifyMLXRuntime(ctx context.Context) error {
	// Verify the installation we just changed, not a possibly broken external override.
	probe := probeMLXRuntimeWithPython(ctx, i.platform, pythonPathResult{Path: managedMLXPython(i.platform)})
	if !probe.ready {
		detail := strings.TrimSpace(probe.detail)
		if detail == "" {
			detail = "mlx-audio import probe failed after install"
		}
		return fmt.Errorf("mlx runtime verification failed: %s", detail)
	}
	return nil
}

func componentSupportedOnHost(p *platformEnv, component string) bool {
	switch component {
	case ComponentMLX:
		return isDarwinARM64(p)
	case ComponentPython, ComponentFFmpeg, ComponentWhisperKit:
		return isDarwin(p)
	default:
		return false
	}
}

func pipInstallEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		key := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			key = kv[:idx]
		}
		switch strings.ToUpper(key) {
		case "PIP_TARGET", "PIP_PREFIX":
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "PIP_REQUIRE_VIRTUALENV=1")
	return out
}

func isSupportedInstallComponent(component string) bool {
	switch component {
	case ComponentPython, ComponentFFmpeg, ComponentWhisperKit, ComponentMLX:
		return true
	default:
		return false
	}
}
