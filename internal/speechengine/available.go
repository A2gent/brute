package speechengine

import (
	"runtime"
	"strings"
)

// Available reports whether an engine's runtime toolchain is present on this host.
// It does not download models or verify model weights.
func Available(engine string) bool {
	engine = normalizeEngineID(engine)
	switch engine {
	case EngineParakeet, EngineMoonshine, EngineKokoro, EngineQwen3TTS:
		return mlxRuntimeAvailable()
	case EngineWhisperKit:
		if runtime.GOOS != "darwin" {
			return false
		}
		return strings.TrimSpace(resolveWhisperKitBin()) != ""
	case EngineWhisperCPP:
		return strings.TrimSpace(resolveWhisperCPPBin()) != ""
	default:
		return false
	}
}

func mlxRuntimeAvailable() bool {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return false
	}
	return strings.TrimSpace(resolvePythonPath()) != ""
}
