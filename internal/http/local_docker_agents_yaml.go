package http

import (
	"encoding/json"
	"net/http"
	"strings"
)

type createLocalDockerAgentsFromYAMLRequest struct {
	ConfigYAML string `json:"config_yaml"`
	ConfigPath string `json:"config_path"`
}

func (s *Server) handleCreateLocalDockerAgentsFromYAML(w http.ResponseWriter, r *http.Request) {
	var req createLocalDockerAgentsFromYAMLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	raw := []byte(req.ConfigYAML)
	configPath := ""
	if strings.TrimSpace(req.ConfigPath) != "" {
		loaded, resolved, err := readLocalDockerAgentYAMLConfigFile(req.ConfigPath, "")
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		raw = loaded
		configPath = resolved
	}
	result, status, err := s.createLocalDockerAgentsFromYAML(r.Context(), raw, configPath)
	if err != nil {
		s.errorResponse(w, status, err.Error())
		return
	}
	s.jsonResponse(w, status, result)
}
