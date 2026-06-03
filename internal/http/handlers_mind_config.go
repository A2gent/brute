package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const mindRootFolderSettingKey = "AAGENT_MY_MIND_ROOT_FOLDER"

func (s *Server) handleGetMindConfig(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load settings: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindConfigResponse{RootFolder: strings.TrimSpace(settings[mindRootFolderSettingKey])})
}

func (s *Server) handleUpdateMindConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateMindConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load settings: "+err.Error())
		return
	}
	if settings == nil {
		settings = map[string]string{}
	}

	rootFolder := strings.TrimSpace(req.RootFolder)
	if rootFolder == "" {
		delete(settings, mindRootFolderSettingKey)
	} else {
		resolvedRoot, err := filepath.Abs(rootFolder)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Invalid root folder path")
			return
		}
		info, err := os.Stat(resolvedRoot)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				s.errorResponse(w, http.StatusBadRequest, "Selected root folder does not exist")
				return
			}
			s.errorResponse(w, http.StatusBadRequest, "Failed to access selected root folder: "+err.Error())
			return
		}
		if !info.IsDir() {
			s.errorResponse(w, http.StatusBadRequest, "Selected root path is not a folder")
			return
		}
		settings[mindRootFolderSettingKey] = resolvedRoot
	}

	if err := s.store.SaveSettings(settings); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save settings: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindConfigResponse{RootFolder: strings.TrimSpace(settings[mindRootFolderSettingKey])})
}

func (s *Server) handleBrowseMindDirectories(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			path = homeDir
		} else {
			path = string(os.PathSeparator)
		}
	}

	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid path")
		return
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to list directory: "+err.Error())
		return
	}

	respEntries := make([]MindTreeEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fullPath := filepath.Join(resolvedPath, entry.Name())
		hasChild := directoryHasChildren(fullPath)
		respEntries = append(respEntries, MindTreeEntry{
			Name:     entry.Name(),
			Path:     fullPath,
			Type:     "directory",
			HasChild: hasChild,
		})
	}

	sort.Slice(respEntries, func(i, j int) bool {
		return strings.ToLower(respEntries[i].Name) < strings.ToLower(respEntries[j].Name)
	})

	s.jsonResponse(w, http.StatusOK, MindTreeResponse{
		RootFolder: resolvedPath,
		Path:       resolvedPath,
		Entries:    respEntries,
	})
}

func (s *Server) loadMindRootFolder() (string, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		return "", errors.New("failed to load settings")
	}

	rootFolder := strings.TrimSpace(settings[mindRootFolderSettingKey])
	if rootFolder == "" {
		return "", errors.New("My Mind root folder is not configured")
	}

	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		return "", errors.New("configured My Mind root folder is invalid")
	}

	info, err := os.Stat(resolvedRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("configured My Mind root folder does not exist")
		}
		return "", errors.New("failed to access configured My Mind root folder")
	}
	if !info.IsDir() {
		return "", errors.New("configured My Mind root path is not a folder")
	}

	return resolvedRoot, nil
}

func resolveMindPath(rootFolder, relPath string) (string, string, error) {
	normalized := filepath.Clean(strings.TrimSpace(relPath))
	if normalized == "." {
		normalized = ""
	}
	if filepath.IsAbs(normalized) {
		return "", "", errors.New("path must be relative to My Mind root")
	}

	resolvedPath := rootFolder
	if normalized != "" {
		resolvedPath = filepath.Join(rootFolder, normalized)
	}
	resolvedPath = filepath.Clean(resolvedPath)

	relToRoot, err := filepath.Rel(rootFolder, resolvedPath)
	if err != nil {
		return "", "", errors.New("invalid path")
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("path escapes My Mind root folder")
	}

	if relToRoot == "." {
		relToRoot = ""
	}

	return resolvedPath, relToRoot, nil
}
