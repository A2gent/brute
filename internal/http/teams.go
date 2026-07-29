package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/teamdef"
	"github.com/go-chi/chi/v5"
)

const defaultTeamsDirectory = "teams"

type teamWriteRequest struct {
	ProjectID   string              `json:"project_id,omitempty"`
	YAML        string              `json:"yaml,omitempty"`
	ConfigYAML  string              `json:"config_yaml,omitempty"`
	Definition  *teamdef.Definition `json:"definition,omitempty"`
	ID          string              `json:"id,omitempty"`
	Name        string              `json:"name,omitempty"`
	Description string              `json:"description,omitempty"`
	Policy      teamdef.Policy      `json:"policy,omitempty"`
	Members     []teamdef.Member    `json:"members,omitempty"`
}

type teamResponse struct {
	ID          string              `json:"id"`
	ProjectID   string              `json:"project_id,omitempty"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Definition  *teamdef.Definition `json:"definition"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func (s *Server) handleListTeams(w http.ResponseWriter, r *http.Request) {
	var projectID *string
	if raw := strings.TrimSpace(r.URL.Query().Get("project_id")); raw != "" {
		projectID = &raw
	}
	records, err := s.store.ListTeams(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list teams: "+err.Error())
		return
	}
	responses := make([]teamResponse, 0, len(records))
	for _, record := range records {
		response, err := teamResponseFromRecord(record)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to parse stored team: "+err.Error())
			return
		}
		responses = append(responses, response)
	}
	s.jsonResponse(w, http.StatusOK, responses)
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	s.handleWriteTeam(w, r, "", true)
}

func (s *Server) handleImportTeamYAML(w http.ResponseWriter, r *http.Request) {
	s.handleWriteTeam(w, r, "", true)
}

func (s *Server) handleUpdateTeam(w http.ResponseWriter, r *http.Request) {
	s.handleWriteTeam(w, r, chi.URLParam(r, "teamID"), false)
}

func (s *Server) handleWriteTeam(w http.ResponseWriter, r *http.Request, pathID string, createOnly bool) {
	var req teamWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	def, raw, err := teamDefinitionFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if pathID != "" && pathID != def.ID {
		s.errorResponse(w, http.StatusBadRequest, "Team definition id must match the team ID in the request path")
		return
	}

	existing, getErr := s.store.GetTeam(def.ID)
	if createOnly && getErr == nil {
		s.errorResponse(w, http.StatusConflict, "Team already exists: "+def.ID)
		return
	}
	if !createOnly && getErr != nil {
		s.errorResponse(w, http.StatusNotFound, "Team not found: "+def.ID)
		return
	}
	if getErr != nil && !errors.Is(getErr, storage.ErrTeamNotFound) {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to read team: "+getErr.Error())
		return
	}

	projectID, err := s.normalizeTeamProjectID(req.ProjectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	createdAt := now
	if existing != nil {
		createdAt = existing.CreatedAt
	}
	record := &storage.TeamRecord{
		ID:             def.ID,
		ProjectID:      projectID,
		Name:           def.Name,
		Description:    def.Description,
		DefinitionYAML: string(raw),
		CreatedAt:      createdAt,
		UpdatedAt:      now,
	}
	if err := s.saveTeamDual(record, existing); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save team: "+err.Error())
		return
	}
	response, _ := teamResponseFromRecord(record)
	status := http.StatusOK
	if existing == nil {
		status = http.StatusCreated
	}
	s.jsonResponse(w, status, response)
}

func (s *Server) handleGetTeam(w http.ResponseWriter, r *http.Request) {
	record, err := s.store.GetTeam(chi.URLParam(r, "teamID"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrTeamNotFound) {
			status = http.StatusNotFound
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	response, err := teamResponseFromRecord(record)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleExportTeamYAML(w http.ResponseWriter, r *http.Request) {
	record, err := s.store.GetTeam(chi.URLParam(r, "teamID"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrTeamNotFound) {
			status = http.StatusNotFound
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"id": record.ID, "yaml": record.DefinitionYAML})
}

func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	record, err := s.store.GetTeam(chi.URLParam(r, "teamID"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrTeamNotFound) {
			status = http.StatusNotFound
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	path, err := s.teamDefinitionPath(record.ProjectID, record.ID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete canonical team YAML: "+err.Error())
		return
	}
	if err := s.store.DeleteTeam(record.ID); err != nil {
		// Restore the canonical source if the index deletion failed.
		_ = writeFileAtomic(path, []byte(record.DefinitionYAML), 0o644)
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete team: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func teamDefinitionFromRequest(req teamWriteRequest) (*teamdef.Definition, []byte, error) {
	rawYAML := strings.TrimSpace(req.YAML)
	if rawYAML == "" {
		rawYAML = strings.TrimSpace(req.ConfigYAML)
	}
	var def *teamdef.Definition
	var err error
	if rawYAML != "" {
		def, err = teamdef.ParseYAML([]byte(rawYAML))
	} else if req.Definition != nil {
		def = req.Definition
	} else {
		def = &teamdef.Definition{ID: req.ID, Name: req.Name, Description: req.Description, Policy: req.Policy, Members: req.Members}
	}
	if err != nil {
		return nil, nil, err
	}
	raw, err := teamdef.ToYAML(def)
	if err != nil {
		return nil, nil, err
	}
	return def, raw, nil
}

func teamResponseFromRecord(record *storage.TeamRecord) (teamResponse, error) {
	if record == nil {
		return teamResponse{}, fmt.Errorf("team record is empty")
	}
	def, err := teamdef.ParseYAML([]byte(record.DefinitionYAML))
	if err != nil {
		return teamResponse{}, fmt.Errorf("stored team %s is invalid: %w", record.ID, err)
	}
	return teamResponse{
		ID:          record.ID,
		ProjectID:   record.ProjectID,
		Name:        record.Name,
		Description: record.Description,
		Definition:  def,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	}, nil
}

func (s *Server) normalizeTeamProjectID(raw string) (string, error) {
	projectID := strings.TrimSpace(raw)
	if projectID == "" {
		return "", nil
	}
	if _, err := s.store.GetProject(projectID); err != nil {
		return "", fmt.Errorf("project not found: %w", err)
	}
	return projectID, nil
}

func (s *Server) teamDefinitionPath(projectID, teamID string) (string, error) {
	var root string
	if projectID == "" {
		root = s.soulProjectFolder()
	} else {
		project, err := s.store.GetProject(projectID)
		if err != nil {
			return "", err
		}
		if project.Folder != nil {
			root = strings.TrimSpace(*project.Folder)
		}
	}
	if root == "" {
		return "", fmt.Errorf("team project folder is not configured")
	}
	return filepath.Join(root, defaultTeamsDirectory, teamID+".yaml"), nil
}

func (s *Server) saveTeamDual(record, existing *storage.TeamRecord) error {
	path, err := s.teamDefinitionPath(record.ProjectID, record.ID)
	if err != nil {
		return err
	}
	previous, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if err := writeFileAtomic(path, []byte(record.DefinitionYAML), 0o644); err != nil {
		return err
	}
	if err := s.store.SaveTeam(record); err != nil {
		if readErr == nil {
			_ = writeFileAtomic(path, previous, 0o644)
		} else {
			_ = os.Remove(path)
		}
		return err
	}
	if existing != nil && existing.ProjectID != record.ProjectID {
		oldPath, oldErr := s.teamDefinitionPath(existing.ProjectID, existing.ID)
		if oldErr == nil && oldPath != path {
			_ = os.Remove(oldPath)
		}
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".team-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
