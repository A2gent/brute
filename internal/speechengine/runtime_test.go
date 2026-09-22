package speechengine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu       sync.Mutex
	calls    [][]string
	handlers map[string]func(args []string) (stdout, stderr []byte, err error)
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	call := append([]string{name}, args...)
	f.mu.Lock()
	f.calls = append(f.calls, call)
	key := strings.Join(call, "\x00")
	handler := f.handlers[key]
	f.mu.Unlock()
	if handler != nil {
		return handler(args)
	}
	if len(args) >= 3 && args[0] == "-m" && args[1] == "venv" {
		venvDir := args[2]
		binDir := filepath.Join(venvDir, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return nil, nil, err
		}
		python := filepath.Join(binDir, "python")
		if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755); err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(filepath.Join(venvDir, "pyvenv.cfg"), []byte("home = /tmp\n"), 0o644); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
	if len(args) >= 2 && args[0] == "-c" {
		if strings.Contains(args[1], "mlx_audio") {
			return []byte(`{"ok":true}`), nil, nil
		}
		if strings.Contains(args[1], "version_info") {
			return []byte("3.11\n"), nil, nil
		}
	}
	if len(args) >= 3 && args[0] == "-m" && args[1] == "pip" && args[2] == "install" {
		return nil, []byte("installed"), nil
	}
	return nil, nil, errors.New("unexpected command: " + strings.Join(call, " "))
}

func withTestPlatform(t *testing.T, p *platformEnv) {
	old := getRuntimePlatform
	getRuntimePlatform = func() *platformEnv { return p }
	t.Cleanup(func() { getRuntimePlatform = old })
}

func testPlatform(t *testing.T, runner commandRunner) *platformEnv {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mkexec := func(name string) string {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ffmpeg := mkexec("ffmpeg")
	whisperkit := mkexec("whisperkit-cli")
	python311 := mkexec("python3.11")
	say := mkexec("say")
	return &platformEnv{
		goos:     "darwin",
		goarch:   "arm64",
		dataPath: dir,
		brewDirs: []string{binDir},
		lookPath: func(name string) (string, error) {
			switch name {
			case "ffmpeg":
				return ffmpeg, nil
			case "whisperkit-cli":
				return whisperkit, nil
			case "python3.11":
				return python311, nil
			case "say":
				return say, nil
			default:
				return "", exec.ErrNotFound
			}
		},
		runner: runner,
	}
}

func TestInspectRuntimeReturnsHostAndDependencies(t *testing.T) {
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	withTestPlatform(t, p)

	status := InspectRuntime(context.Background())
	if status.OS != "darwin" || status.Arch != "arm64" {
		t.Fatalf("unexpected host: %s/%s", status.OS, status.Arch)
	}
	if status.Job != nil {
		t.Fatal("InspectRuntime must not populate job")
	}
	if len(status.Dependencies) != 4 {
		t.Fatalf("expected 4 dependencies, got %d", len(status.Dependencies))
	}
	if len(status.Engines) < 8 {
		t.Fatalf("expected at least 8 engines, got %d", len(status.Engines))
	}

	byID := map[string]DependencyStatus{}
	for _, dep := range status.Dependencies {
		byID[dep.ID] = dep
	}
	if !byID[ComponentPython].Installed || byID[ComponentPython].Path == "" {
		t.Fatalf("expected python installed, got %+v", byID[ComponentPython])
	}
	if !byID[ComponentFFmpeg].Installed {
		t.Fatalf("expected ffmpeg installed, got %+v", byID[ComponentFFmpeg])
	}
	if !byID[ComponentWhisperKit].Installed {
		t.Fatalf("expected whisperkit installed, got %+v", byID[ComponentWhisperKit])
	}
}

func writeManagedVenvPython(t *testing.T, dir string) string {
	venvRoot := filepath.Join(dir, managedMLXVenvRel)
	venvDir := filepath.Join(venvRoot, "bin")
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvRoot, "pyvenv.cfg"), []byte("home = /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pythonPath := filepath.Join(venvDir, "python")
	if err := os.WriteFile(pythonPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return pythonPath
}

func TestInspectRuntimeMLXReadyWhenProbeSucceeds(t *testing.T) {
	dir := t.TempDir()
	writeManagedVenvPython(t, dir)

	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	p.dataPath = dir
	withTestPlatform(t, p)

	status := InspectRuntime(context.Background())
	mlxDep := findDependency(status.Dependencies, ComponentMLX)
	if !mlxDep.Installed {
		t.Fatalf("expected mlx dependency installed, got %+v", mlxDep)
	}
	for _, id := range []string{EngineParakeet, EngineMoonshine, EngineKokoro, EngineQwen3TTS} {
		eng := findEngine(status.Engines, id)
		if !eng.RuntimeReady {
			t.Fatalf("expected %s runtime ready, got %+v", id, eng)
		}
	}
}

func TestInspectRuntimeLegacyEnginesNotFakedReady(t *testing.T) {
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	p.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	withTestPlatform(t, p)

	status := InspectRuntime(context.Background())
	for _, id := range []string{EngineWhisperCPP, "piper_tts"} {
		eng := findEngine(status.Engines, id)
		if !eng.Supported {
			t.Fatalf("expected %s supported", id)
		}
		if eng.RuntimeReady {
			t.Fatalf("expected %s not faked ready", id)
		}
	}
	macos := findEngine(status.Engines, "macos_say_tts")
	if !macos.Supported || macos.RuntimeReady {
		t.Fatalf("expected macos_say_tts supported without fake ready, got %+v", macos)
	}
}

func TestInstallerRejectsUnknownComponent(t *testing.T) {
	inst := NewInstaller()
	_, err := inst.Start("unknown")
	if !errors.Is(err, ErrUnsupportedComponent) {
		t.Fatalf("expected ErrUnsupportedComponent, got %v", err)
	}
}

func TestInstallerRejectsConcurrentStart(t *testing.T) {
	dir := t.TempDir()
	brewPath := filepath.Join(dir, "brew")
	if err := os.WriteFile(brewPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := testPlatform(t, &fakeRunner{})
	p.brewPath = brewPath
	withTestPlatform(t, p)

	inst := &Installer{platform: p}
	block := make(chan struct{})
	inst.platform.runner = &blockingRunner{block: block, brewPath: brewPath}

	job, err := inst.Start(ComponentFFmpeg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if job.State != installJobStateRunning {
		t.Fatalf("expected running job, got %+v", job)
	}

	_, err = inst.Start(ComponentPython)
	if !errors.Is(err, ErrInstallBusy) {
		t.Fatalf("expected ErrInstallBusy, got %v", err)
	}

	close(block)
	waitForJob(t, inst, installJobStateSucceeded)
}

func TestInstallerHomebrewUsesFixedArgs(t *testing.T) {
	dir := t.TempDir()
	brewPath := filepath.Join(dir, "brew")
	if err := os.WriteFile(brewPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){
		brewPath + "\x00install\x00ffmpeg": func([]string) (stdout, stderr []byte, err error) {
			return nil, []byte("installed ffmpeg"), nil
		},
	}}
	p := testPlatform(t, runner)
	p.brewPath = brewPath
	withTestPlatform(t, p)

	inst := &Installer{platform: p}
	_, err := inst.Start(ComponentFFmpeg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitForJob(t, inst, installJobStateSucceeded)

	found := false
	for _, call := range runner.calls {
		if len(call) >= 3 && call[0] == brewPath && call[1] == "install" && call[2] == "ffmpeg" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected brew install ffmpeg, calls=%v", runner.calls)
	}
}

func TestInstallerMLXCreatesManagedVenv(t *testing.T) {
	dir := t.TempDir()
	bootstrap := filepath.Join(dir, "python3.11")
	if err := os.WriteFile(bootstrap, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	p.dataPath = dir
	p.lookPath = func(name string) (string, error) {
		if name == "python3.11" {
			return bootstrap, nil
		}
		return "", exec.ErrNotFound
	}
	withTestPlatform(t, p)

	inst := &Installer{platform: p}
	_, err := inst.Start(ComponentMLX)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitForJob(t, inst, installJobStateSucceeded)

	venvPython := filepath.Join(dir, managedMLXVenvRel, "bin", "python")
	foundVenv := false
	foundPip := false
	for _, call := range runner.calls {
		if len(call) >= 4 && call[1] == "-m" && call[2] == "venv" && call[3] == filepath.Join(dir, managedMLXVenvRel) {
			foundVenv = true
		}
		if len(call) >= 7 && strings.HasSuffix(call[0], "python") && call[1] == "-m" && call[2] == "pip" && call[3] == "install" && call[4] == "--require-virtualenv" {
			if call[5] == mlxAudioPackagePin && call[6] == misakiPackagePin {
				foundPip = true
			}
		}
	}
	if !foundVenv {
		t.Fatalf("expected venv creation, calls=%v", runner.calls)
	}
	if !foundPip {
		t.Fatalf("expected pinned pip install, calls=%v", runner.calls)
	}
	_ = venvPython
}

func TestProbeMLXRuntimePrefersExplicitOverrideOverManaged(t *testing.T) {
	dir := t.TempDir()
	writeManagedVenvPython(t, dir)
	override := filepath.Join(t.TempDir(), "custom-python")
	if err := os.WriteFile(override, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	p.dataPath = dir
	withTestPlatform(t, p)

	t.Setenv("AAGENT_SPEECH_PYTHON", override)
	probe := probeMLXRuntime(context.Background(), p)
	if probe.path != override {
		t.Fatalf("expected override python %q, got %q", override, probe.path)
	}
}

func TestProbeMLXRuntimeUsesPATHFallbackWithoutManaged(t *testing.T) {
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	withTestPlatform(t, p)

	probe := probeMLXRuntime(context.Background(), p)
	if !probe.ready {
		t.Fatalf("expected PATH fallback probe ready, got %+v", probe)
	}
	if !strings.HasSuffix(probe.path, "python3.11") {
		t.Fatalf("expected python3.11 fallback, got %q", probe.path)
	}
}

func TestProbeMLXRuntimeRejectsLowVersionOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-python")
	if err := os.WriteFile(override, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	versionScript := "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')"
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){
		strings.Join([]string{override, "-c", versionScript}, "\x00"): func([]string) (stdout, stderr []byte, err error) {
			return []byte("3.9\n"), nil, nil
		},
	}}
	p := testPlatform(t, runner)
	withTestPlatform(t, p)

	t.Setenv("AAGENT_SPEECH_PYTHON", override)
	probe := probeMLXRuntime(context.Background(), p)
	if probe.ready {
		t.Fatal("expected low-version override to fail probe")
	}
	if !strings.Contains(probe.detail, "below 3.10") {
		t.Fatalf("expected version error, got %q", probe.detail)
	}
}

func TestProbeMLXRuntimeRejectsInvalidOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(override, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	withTestPlatform(t, p)

	t.Setenv("AAGENT_SPEECH_PYTHON", override)
	probe := probeMLXRuntime(context.Background(), p)
	if probe.ready {
		t.Fatal("expected invalid override to fail probe")
	}
	if !strings.Contains(probe.detail, "not an executable") {
		t.Fatalf("expected executable error, got %q", probe.detail)
	}
}

func TestInspectRuntimeFindsManagedWhisperCPPBinary(t *testing.T) {
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	p.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	want := filepath.Join(p.dataPath, "speech", "whisper", "build", "bin", "whisper-cli")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_WHISPER_BIN", "")
	withTestPlatform(t, p)

	status := InspectRuntime(context.Background())
	eng := findEngine(status.Engines, EngineWhisperCPP)
	if !eng.RuntimeReady {
		t.Fatalf("expected whisper.cpp ready from managed binary, got %+v", eng)
	}
	if !strings.Contains(eng.Detail, want) {
		t.Fatalf("expected managed binary path in detail, got %q", eng.Detail)
	}
}

func TestInspectRuntimeFindsBrewWhisperCPPBinary(t *testing.T) {
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	p.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	want := filepath.Join(p.brewDirs[0], "whisper-cli")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_WHISPER_BIN", "")
	withTestPlatform(t, p)

	status := InspectRuntime(context.Background())
	eng := findEngine(status.Engines, EngineWhisperCPP)
	if !eng.RuntimeReady {
		t.Fatalf("expected whisper.cpp ready from brew binary, got %+v", eng)
	}
	if !strings.Contains(eng.Detail, want) {
		t.Fatalf("expected brew binary path in detail, got %q", eng.Detail)
	}
}

func TestResolveWhisperKitBinUsesHomebrewFallback(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(binDir, "whisperkit-cli")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := testPlatform(t, &fakeRunner{})
	p.brewDirs = []string{binDir}
	p.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	withTestPlatform(t, p)

	got := resolveWhisperKitBin()
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestDiscoverBootstrapPythonAcceptsPython310OnPath(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	python310 := filepath.Join(binDir, "python3")
	if err := os.WriteFile(python310, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	p.brewDirs = []string{t.TempDir()}
	p.lookPath = func(name string) (string, error) {
		if name == "python3" {
			return python310, nil
		}
		return "", exec.ErrNotFound
	}

	got := discoverBootstrapPython(context.Background(), p)
	if got != python310 {
		t.Fatalf("expected python3.10 fallback %q, got %q", python310, got)
	}
}

func TestInstallerRejectsUnsupportedPlatform(t *testing.T) {
	p := testPlatform(t, &fakeRunner{})
	p.goos = "linux"
	p.goarch = "amd64"
	withTestPlatform(t, p)

	inst := &Installer{platform: p}
	_, err := inst.Start(ComponentFFmpeg)
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("expected ErrUnsupportedPlatform, got %v", err)
	}
}

func TestInstallerStartReturnsStableSnapshotBeforeGoroutine(t *testing.T) {
	block := make(chan struct{})
	runner := &blockingRunner{block: block}
	p := testPlatform(t, runner)
	dir := t.TempDir()
	brewPath := filepath.Join(dir, "brew")
	if err := os.WriteFile(brewPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.brewPath = brewPath
	runner.brewPath = brewPath
	withTestPlatform(t, p)

	inst := &Installer{platform: p}
	job, err := inst.Start(ComponentFFmpeg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if job.State != installJobStateRunning {
		t.Fatalf("expected running snapshot, got %+v", job)
	}
	if job.Log != "" {
		t.Fatalf("expected empty initial log snapshot, got %q", job.Log)
	}
	close(block)
	waitForJob(t, inst, installJobStateSucceeded)
}

func TestInstallerStreamsCommandOutput(t *testing.T) {
	dir := t.TempDir()
	brewPath := filepath.Join(dir, "brew")
	if err := os.WriteFile(brewPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &streamingFakeRunner{
		handlers: map[string]func([]string, streamAppender) error{
			brewPath + "\x00install\x00ffmpeg": func(_ []string, appendLine streamAppender) error {
				appendLine("Downloading ffmpeg")
				appendLine("Installed ffmpeg")
				return nil
			},
		},
	}
	p := testPlatform(t, runner)
	p.brewPath = brewPath
	withTestPlatform(t, p)

	inst := &Installer{platform: p}
	_, err := inst.Start(ComponentFFmpeg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitForJob(t, inst, installJobStateSucceeded)

	job := inst.Job()
	if job == nil || !strings.Contains(job.Log, "Downloading ffmpeg") {
		t.Fatalf("expected streamed log output, got %+v", job)
	}
}

func TestInspectMLXDependencyMentionsOverrideWhenManagedReady(t *testing.T) {
	dir := t.TempDir()
	_ = writeManagedVenvPython(t, dir)
	override := filepath.Join(t.TempDir(), "custom-python")
	if err := os.WriteFile(override, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	p := testPlatform(t, runner)
	p.dataPath = dir
	withTestPlatform(t, p)
	t.Setenv("AAGENT_SPEECH_PYTHON", override)

	status := InspectRuntime(context.Background())
	mlxDep := findDependency(status.Dependencies, ComponentMLX)
	if !strings.Contains(mlxDep.Detail, "clear AAGENT_SPEECH_PYTHON") {
		t.Fatalf("expected override guidance, got %+v", mlxDep)
	}
}

func TestMLXEngineStatusRespectsInjectedPlatform(t *testing.T) {
	p := &platformEnv{goos: "linux", goarch: "amd64"}
	eng := mlxEngineStatus(p, EngineParakeet, "Parakeet", "stt", "model", ComponentMLX, false)
	if eng.Supported {
		t.Fatal("expected mlx engine unsupported on injected linux platform")
	}
}

func TestResolvePythonPathPrefersManagedVenvAfterOverride(t *testing.T) {
	dir := t.TempDir()
	managed := writeManagedVenvPython(t, dir)

	runner := &fakeRunner{handlers: map[string]func([]string) (stdout, stderr []byte, err error){}}
	oldPlatform := getRuntimePlatform
	getRuntimePlatform = func() *platformEnv {
		return &platformEnv{dataPath: dir, runner: runner}
	}
	t.Cleanup(func() { getRuntimePlatform = oldPlatform })

	override := filepath.Join(t.TempDir(), "custom-python")
	if err := os.WriteFile(override, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_SPEECH_PYTHON", override)
	if got := resolvePythonPath(); got != override {
		t.Fatalf("expected override %q, got %q", override, got)
	}

	t.Setenv("AAGENT_SPEECH_PYTHON", "")
	if got := resolvePythonPath(); got != managed {
		t.Fatalf("expected managed python %q, got %q", managed, got)
	}
}

func TestRuntimeStatusJSONShape(t *testing.T) {
	status := RuntimeStatus{
		OS:   "darwin",
		Arch: "arm64",
		Dependencies: []DependencyStatus{{
			ID: "python", Label: "Python", Installed: true, Supported: true, Installable: false, Path: "/tmp/py", Detail: "ok",
		}},
		Engines: []EngineStatus{{
			ID: "parakeet", Label: "Parakeet", Kind: "stt", Model: "m", RuntimeID: "mlx", Detail: "ready", Supported: true, RuntimeReady: true,
		}},
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	for _, key := range []string{"\"os\"", "\"arch\"", "\"dependencies\"", "\"engines\"", "\"runtime_ready\"", "\"runtime_id\""} {
		if !strings.Contains(payload, key) {
			t.Fatalf("missing %s in %s", key, payload)
		}
	}
	if strings.Contains(payload, "\"job\":") && !strings.Contains(payload, "\"job\":null") {
		// job omitted or null is fine
	}
}

type blockingRunner struct {
	block    chan struct{}
	brewPath string
}

func (b *blockingRunner) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	if name != b.brewPath {
		return nil, nil, errors.New("unexpected command")
	}
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-b.block:
		return nil, []byte("done"), nil
	}
}

type streamingFakeRunner struct {
	mu       sync.Mutex
	calls    [][]string
	handlers map[string]func(args []string, appendLine streamAppender) error
}

func (f *streamingFakeRunner) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	err = f.RunStreaming(ctx, name, args, nil, nil)
	return nil, nil, err
}

func (f *streamingFakeRunner) RunStreaming(ctx context.Context, name string, args []string, appendLine streamAppender, env []string) error {
	call := append([]string{name}, args...)
	f.mu.Lock()
	f.calls = append(f.calls, call)
	key := strings.Join(call, "\x00")
	handler := f.handlers[key]
	f.mu.Unlock()
	if handler != nil {
		return handler(args, appendLine)
	}
	return errors.New("unexpected command: " + strings.Join(call, " "))
}

func waitForJob(t *testing.T, inst *Installer, wantState string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job := inst.Job()
		if job != nil && job.State == wantState {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	job := inst.Job()
	t.Fatalf("expected job state %s, got %+v", wantState, job)
}

func findDependency(deps []DependencyStatus, id string) DependencyStatus {
	for _, dep := range deps {
		if dep.ID == id {
			return dep
		}
	}
	return DependencyStatus{}
}

func findEngine(engines []EngineStatus, id string) EngineStatus {
	for _, eng := range engines {
		if eng.ID == id {
			return eng
		}
	}
	return EngineStatus{}
}

func TestFFmpegUsesSameHomebrewResolutionAsDiagnostics(t *testing.T) {
	runner := &fakeRunner{handlers: map[string]func([]string) ([]byte, []byte, error){}}
	p := testPlatform(t, runner)
	dir := t.TempDir()
	p.brewDirs = []string{dir}
	p.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	withTestPlatform(t, p)
	if FFmpegPath() != path || inspectFFmpegDependency(p).Path != path {
		t.Fatal("runtime/diagnostics ffmpeg mismatch")
	}
}

func TestMLXProbeScriptSucceedsWithStubbedPackage(t *testing.T) {
	python := lookPathPython3(t)
	site := t.TempDir()
	writeStubMLXAudio(t, site, "0.5.4")

	out, err := runMLXProbeScript(python, site)
	if err != nil {
		t.Fatalf("probe script failed: %v\n%s", err, out)
	}
	payload := decodeProbeJSON(t, out)
	if !payload.OK {
		t.Fatalf("expected ok probe, got %+v from %s", payload, out)
	}
}

func TestMLXProbeScriptReportsMissingPackageWithoutNameError(t *testing.T) {
	python := lookPathPython3(t)
	out, runErr := runMLXProbeScript(python, t.TempDir())
	if runErr == nil {
		t.Fatalf("expected missing mlx-audio to fail, got %s", out)
	}
	text := string(out)
	if strings.Contains(text, "name 'true'") || strings.Contains(text, "name 'false'") {
		t.Fatalf("probe used JSON booleans inside Python: %s", text)
	}
	payload := decodeProbeJSON(t, out)
	if payload.OK {
		t.Fatalf("expected failed probe, got %s", out)
	}
	if !strings.Contains(strings.ToLower(payload.Detail), "mlx_audio") && !strings.Contains(strings.ToLower(payload.Detail), "mlx-audio") {
		t.Fatalf("expected mlx-audio missing detail, got %s", out)
	}
}

func lookPathPython3(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3.13", "python3.12", "python3.11", "python3.10", "python3"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if err := exec.Command(path, "-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 10) else 1)").Run(); err == nil {
			return path
		}
	}
	t.Skip("Python 3.10+ not available")
	return ""
}

func writeStubMLXAudio(t *testing.T, site, version string) {
	t.Helper()
	writeFile := func(rel, body string) {
		path := filepath.Join(site, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("mlx_audio/__init__.py", "")
	writeFile("mlx_audio/stt/__init__.py", "")
	writeFile("mlx_audio/stt/utils.py", "def load(*args, **kwargs):\n    return None\n")
	writeFile("mlx_audio/tts/__init__.py", "")
	writeFile("mlx_audio/tts/utils.py", "def load_model(*args, **kwargs):\n    return None\n")
	writeFile("mlx_audio-"+version+".dist-info/METADATA", "Name: mlx-audio\nVersion: "+version+"\n")
	writeFile("mlx_audio-"+version+".dist-info/RECORD", "")
}

func runMLXProbeScript(python, pythonPath string) ([]byte, error) {
	cmd := exec.Command(python, "-c", mlxProbeScript)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+pythonPath, "PYTHONNOUSERSITE=1")
	return cmd.CombinedOutput()
}

func decodeProbeJSON(t *testing.T, raw []byte) struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
} {
	t.Helper()
	var payload struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	trimmed := strings.TrimSpace(string(raw))
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		t.Fatalf("invalid probe json %q: %v", raw, err)
	}
	return payload
}
