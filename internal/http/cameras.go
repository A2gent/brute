package http

import (
	"net/http"

	"github.com/A2gent/brute/internal/tools"
)

type CameraDevicesResponse struct {
	Cameras []tools.CameraDevice `json:"cameras"`
}

func (s *Server) handleListCameraDevices(w http.ResponseWriter, r *http.Request) {
	cameras, err := tools.ListCameraDevices(r.Context())
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list camera devices: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, CameraDevicesResponse{
		Cameras: cameras,
	})
}
