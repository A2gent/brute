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

type MindConfigResponse struct {
	RootFolder string `json:"root_folder"`
}

type UpdateMindConfigRequest struct {
	RootFolder string `json:"root_folder"`
}

type MindTreeEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	HasChild bool   `json:"has_child,omitempty"`
}

type MindTreeResponse struct {
	RootFolder string          `json:"root_folder"`
	Path       string          `json:"path"`
	Entries    []MindTreeEntry `json:"entries"`
}

type MindFileResponse struct {
	RootFolder string `json:"root_folder"`
	Path       string `json:"path"`
	Content    string `json:"content"`
}

type UpdateMindFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

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
		if strings.HasPrefix(entry.Name(), ".") {
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

func (s *Server) handleListMindTree(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(rootFolder, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to list directory: "+err.Error())
		return
	}

	respEntries := make([]MindTreeEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !entry.IsDir() && !isMarkdownFile(name) {
			continue
		}

		entryRelPath := name
		if normalizedRelPath != "" {
			entryRelPath = filepath.Join(normalizedRelPath, name)
		}

		if entry.IsDir() {
			respEntries = append(respEntries, MindTreeEntry{
				Name:     name,
				Path:     filepath.ToSlash(entryRelPath),
				Type:     "directory",
				HasChild: directoryHasChildren(filepath.Join(resolvedPath, name)),
			})
			continue
		}

		respEntries = append(respEntries, MindTreeEntry{
			Name: name,
			Path: filepath.ToSlash(entryRelPath),
			Type: "file",
		})
	}

	sort.Slice(respEntries, func(i, j int) bool {
		if respEntries[i].Type != respEntries[j].Type {
			return respEntries[i].Type == "directory"
		}
		return strings.ToLower(respEntries[i].Name) < strings.ToLower(respEntries[j].Name)
	})

	s.jsonResponse(w, http.StatusOK, MindTreeResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
		Entries:    respEntries,
	})
}

func (s *Server) handleGetMindFile(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(rootFolder, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isMarkdownFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files can be opened")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+err.Error())
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to read file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    string(content),
	})
}

func (s *Server) handleUpsertMindFile(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req UpdateMindFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	resolvedPath, normalizedRelPath, err := resolveMindPath(rootFolder, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isMarkdownFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files can be created or edited")
		return
	}

	parentDir := filepath.Dir(resolvedPath)
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Parent folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access parent folder: "+err.Error())
		return
	}
	if !parentInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Parent path is not a folder")
		return
	}

	if info, statErr := os.Stat(resolvedPath); statErr == nil && info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+statErr.Error())
		return
	}

	if err := os.WriteFile(resolvedPath, []byte(req.Content), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to write file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    req.Content,
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

func isMarkdownFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

func directoryHasChildren(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.IsDir() || isMarkdownFile(entry.Name()) {
			return true
		}
	}
	return false
}
