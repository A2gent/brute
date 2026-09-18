package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/A2gent/brute/internal/speechengine"
)

type speechRuntimeInstaller interface {
	Start(string) (speechengine.InstallJob, error)
	Job() *speechengine.InstallJob
}

func (s *Server) speechInstaller() speechRuntimeInstaller {
	s.speechRuntimeMu.Lock()
	defer s.speechRuntimeMu.Unlock()
	if s.speechRuntimeInstaller == nil {
		s.speechRuntimeInstaller = speechengine.NewInstaller()
	}
	return s.speechRuntimeInstaller
}

func (s *Server) handleSpeechRuntime(w http.ResponseWriter, r *http.Request) {
	inspect := s.inspectSpeechRuntime
	if inspect == nil {
		inspect = speechengine.InspectRuntime
	}
	status := inspect(r.Context())
	status.Job = s.speechInstaller().Job()
	s.jsonResponse(w, http.StatusOK, status)
}

func (s *Server) handleSpeechRuntimeInstallJob(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"job": s.speechInstaller().Job()})
}

func (s *Server) handleInstallSpeechRuntime(w http.ResponseWriter, r *http.Request) {
	// Require JSON so a cross-origin HTML form cannot trigger a package install.
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.errorResponse(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var req struct {
		Component string `json:"component"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid install request: "+err.Error())
		return
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Expected one JSON object")
		return
	}
	switch req.Component {
	case "python", "ffmpeg", "mlx", "whisperkit":
	default:
		s.errorResponse(w, http.StatusBadRequest, "Unknown speech dependency")
		return
	}
	job, err := s.speechInstaller().Start(req.Component)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, speechengine.ErrInstallBusy) {
			status = http.StatusConflict
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusAccepted, map[string]interface{}{"job": job})
}

// Named type keeps tests able to inject diagnostics without probing host Python.
type speechRuntimeInspector func(context.Context) speechengine.RuntimeStatus
