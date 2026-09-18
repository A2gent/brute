package speechengine

import "errors"

const (
	ComponentPython     = "python"
	ComponentFFmpeg     = "ffmpeg"
	ComponentWhisperKit = "whisperkit"
	ComponentMLX        = "mlx"

	installJobStateRunning   = "running"
	installJobStateSucceeded = "succeeded"
	installJobStateFailed    = "failed"

	managedMLXVenvRel   = "speech/mlx/runtime"
	mlxAudioPackagePin  = "mlx-audio[stt,tts]==0.5.4"
	misakiPackagePin    = "misaki[en]"
	maxInstallLogBytes  = 256 * 1024
	defaultInspectLimit = 30
	pythonProbeTimeout  = 5
)

var (
	ErrInstallBusy          = errors.New("speech runtime install already in progress")
	ErrUnsupportedComponent = errors.New("unsupported speech runtime component")
	ErrUnsupportedPlatform  = errors.New("speech runtime install is not supported on this host")
)

// RuntimeStatus is the public diagnostic snapshot for speech runtimes.
type RuntimeStatus struct {
	OS           string             `json:"os"`
	Arch         string             `json:"arch"`
	Dependencies []DependencyStatus `json:"dependencies"`
	Engines      []EngineStatus     `json:"engines"`
	Job          *InstallJob        `json:"job"`
}

// DependencyStatus describes one installable speech dependency.
type DependencyStatus struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Installed   bool   `json:"installed"`
	Supported   bool   `json:"supported"`
	Installable bool   `json:"installable"`
	Path        string `json:"path"`
	Detail      string `json:"detail"`
}

// EngineStatus describes one speech engine and whether its runtime is ready.
type EngineStatus struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Kind         string `json:"kind"`
	Model        string `json:"model"`
	RuntimeID    string `json:"runtime_id"`
	Detail       string `json:"detail"`
	Supported    bool   `json:"supported"`
	RuntimeReady bool   `json:"runtime_ready"`
}

// InstallJob tracks one asynchronous install operation.
type InstallJob struct {
	ID         string `json:"id"`
	Component  string `json:"component"`
	State      string `json:"state"`
	Log        string `json:"log"`
	Error      string `json:"error"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}
