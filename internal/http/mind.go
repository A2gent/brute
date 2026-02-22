package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

type MindFileDeleteResponse struct {
	RootFolder string `json:"root_folder"`
	Path       string `json:"path"`
}

type UpdateMindFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type MoveMindFileRequest struct {
	FromPath string `json:"from_path"`
	ToPath   string `json:"to_path"`
}

type MoveMindFileResponse struct {
	RootFolder string `json:"root_folder"`
	FromPath   string `json:"from_path"`
	ToPath     string `json:"to_path"`
}

type CreateFolderRequest struct {
	Path string `json:"path"`
}

type CreateFolderResponse struct {
	RootFolder string `json:"root_folder"`
	Path       string `json:"path"`
}

type RenameEntryRequest struct {
	OldPath string `json:"old_path"`
	NewName string `json:"new_name"`
}

type RenameEntryResponse struct {
	RootFolder string `json:"root_folder"`
	OldPath    string `json:"old_path"`
	NewPath    string `json:"new_path"`
}

type ProjectGitChangedFile struct {
	Path           string `json:"path"`
	Status         string `json:"status"`
	IndexStatus    string `json:"index_status"`
	WorktreeStatus string `json:"worktree_status"`
	Staged         bool   `json:"staged"`
	Untracked      bool   `json:"untracked"`
}

type ProjectGitStatusResponse struct {
	RootFolder string                  `json:"root_folder"`
	HasGit     bool                    `json:"has_git"`
	Files      []ProjectGitChangedFile `json:"files"`
}

type ProjectGitCommitRequest struct {
	Message  string `json:"message"`
	RepoPath string `json:"repo_path,omitempty"`
}

type ProjectGitFileRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
	Path     string `json:"path"`
}

type ProjectGitCommitResponse struct {
	RootFolder     string `json:"root_folder"`
	Commit         string `json:"commit"`
	FilesCommitted int    `json:"files_committed"`
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

func (s *Server) handleDeleteMindFile(w http.ResponseWriter, r *http.Request) {
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
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files can be deleted")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "File does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+err.Error())
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	}

	if err := os.Remove(resolvedPath); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileDeleteResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
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

func (s *Server) handleMoveMindFile(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req MoveMindFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	fromResolved, fromNormalized, err := resolveMindPath(rootFolder, req.FromPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid source path: "+err.Error())
		return
	}
	if fromNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Source path is required")
		return
	}

	fromInfo, err := os.Stat(fromResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "Source does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access source: "+err.Error())
		return
	}

	isDir := fromInfo.IsDir()
	if !isDir && !isMarkdownFile(fromNormalized) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files and folders can be moved")
		return
	}

	toResolved, toNormalized, err := resolveMindPath(rootFolder, req.ToPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid destination path: "+err.Error())
		return
	}
	if toNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Destination path is required")
		return
	}

	if !isDir && !isMarkdownFile(toNormalized) {
		s.errorResponse(w, http.StatusBadRequest, "Destination must be a markdown file")
		return
	}

	if fromResolved == toResolved {
		s.errorResponse(w, http.StatusBadRequest, "Source and destination paths are the same")
		return
	}

	if isDir && strings.HasPrefix(toResolved+string(os.PathSeparator), fromResolved+string(os.PathSeparator)) {
		s.errorResponse(w, http.StatusBadRequest, "Cannot move a folder into itself")
		return
	}

	toParentDir := filepath.Dir(toResolved)
	toParentInfo, err := os.Stat(toParentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Destination folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access destination folder: "+err.Error())
		return
	}
	if !toParentInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Destination parent path is not a folder")
		return
	}

	if _, err := os.Stat(toResolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder already exists at the destination path")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check destination: "+err.Error())
		return
	}

	if err := os.Rename(fromResolved, toResolved); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to move: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MoveMindFileResponse{
		RootFolder: rootFolder,
		FromPath:   filepath.ToSlash(fromNormalized),
		ToPath:     filepath.ToSlash(toNormalized),
	})
}

func (s *Server) handleCreateMindFolder(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req CreateFolderRequest
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
		s.errorResponse(w, http.StatusBadRequest, "Folder path is required")
		return
	}

	if _, err := os.Stat(resolvedPath); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder already exists at this path")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check path: "+err.Error())
		return
	}

	if err := os.MkdirAll(resolvedPath, 0o755); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create folder: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, CreateFolderResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
	})
}

func (s *Server) handleRenameMindEntry(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req RenameEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	oldResolved, oldNormalized, err := resolveMindPath(rootFolder, req.OldPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid path: "+err.Error())
		return
	}
	if oldNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Path is required")
		return
	}

	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		s.errorResponse(w, http.StatusBadRequest, "New name is required")
		return
	}
	if strings.ContainsAny(newName, "/\\") {
		s.errorResponse(w, http.StatusBadRequest, "Name cannot contain path separators")
		return
	}

	info, err := os.Stat(oldResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "File or folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access path: "+err.Error())
		return
	}

	if !info.IsDir() && !isMarkdownFile(newName) {
		s.errorResponse(w, http.StatusBadRequest, "File must have .md or .markdown extension")
		return
	}

	parentDir := filepath.Dir(oldResolved)
	newResolved := filepath.Join(parentDir, newName)

	if oldResolved == newResolved {
		s.errorResponse(w, http.StatusBadRequest, "New name is the same as the old name")
		return
	}

	if _, err := os.Stat(newResolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder with this name already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check new path: "+err.Error())
		return
	}

	if err := os.Rename(oldResolved, newResolved); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to rename: "+err.Error())
		return
	}

	newRelPath, err := filepath.Rel(rootFolder, newResolved)
	if err != nil {
		newRelPath = newName
	}

	s.jsonResponse(w, http.StatusOK, RenameEntryResponse{
		RootFolder: rootFolder,
		OldPath:    filepath.ToSlash(oldNormalized),
		NewPath:    filepath.ToSlash(newRelPath),
	})
}

// handleListProjectTree lists files and folders in a project's folder
func (s *Server) handleListProjectTree(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	info, err := os.Stat(resolvedRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Project folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access project folder: "+err.Error())
		return
	}
	if !info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is not a directory")
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, relPath)
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
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
		Entries:    respEntries,
	})
}

// handleGetProjectFile retrieves a file from a project's folder
func (s *Server) handleGetProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, relPath)
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
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    string(content),
	})
}

// handleUpsertProjectFile creates or updates a file in a project's folder
func (s *Server) handleUpsertProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	var req UpdateMindFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, req.Path)
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
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    req.Content,
	})
}

// handleDeleteProjectFile deletes a file from a project's folder
func (s *Server) handleDeleteProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isMarkdownFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files can be deleted")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "File does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+err.Error())
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	}

	if err := os.Remove(resolvedPath); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileDeleteResponse{
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
	})
}

// handleMoveProjectFile moves a file or folder within a project's folder
func (s *Server) handleMoveProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	var req MoveMindFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	fromResolved, fromNormalized, err := resolveMindPath(resolvedRoot, req.FromPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid source path: "+err.Error())
		return
	}
	if fromNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Source path is required")
		return
	}

	fromInfo, err := os.Stat(fromResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "Source does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access source: "+err.Error())
		return
	}

	isDir := fromInfo.IsDir()
	if !isDir && !isMarkdownFile(fromNormalized) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files and folders can be moved")
		return
	}

	toResolved, toNormalized, err := resolveMindPath(resolvedRoot, req.ToPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid destination path: "+err.Error())
		return
	}
	if toNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Destination path is required")
		return
	}

	if !isDir && !isMarkdownFile(toNormalized) {
		s.errorResponse(w, http.StatusBadRequest, "Destination must be a markdown file")
		return
	}

	if fromResolved == toResolved {
		s.errorResponse(w, http.StatusBadRequest, "Source and destination paths are the same")
		return
	}

	if isDir && strings.HasPrefix(toResolved+string(os.PathSeparator), fromResolved+string(os.PathSeparator)) {
		s.errorResponse(w, http.StatusBadRequest, "Cannot move a folder into itself")
		return
	}

	toParentDir := filepath.Dir(toResolved)
	toParentInfo, err := os.Stat(toParentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Destination folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access destination folder: "+err.Error())
		return
	}
	if !toParentInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Destination parent path is not a folder")
		return
	}

	if _, err := os.Stat(toResolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder already exists at the destination path")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check destination: "+err.Error())
		return
	}

	if err := os.Rename(fromResolved, toResolved); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to move: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MoveMindFileResponse{
		RootFolder: resolvedRoot,
		FromPath:   filepath.ToSlash(fromNormalized),
		ToPath:     filepath.ToSlash(toNormalized),
	})
}

func (s *Server) handleCreateProjectFolder(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	var req CreateFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "Folder path is required")
		return
	}

	if _, err := os.Stat(resolvedPath); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder already exists at this path")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check path: "+err.Error())
		return
	}

	if err := os.MkdirAll(resolvedPath, 0o755); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create folder: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, CreateFolderResponse{
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
	})
}

func (s *Server) handleRenameProjectEntry(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	var req RenameEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	oldResolved, oldNormalized, err := resolveMindPath(resolvedRoot, req.OldPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid path: "+err.Error())
		return
	}
	if oldNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Path is required")
		return
	}

	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		s.errorResponse(w, http.StatusBadRequest, "New name is required")
		return
	}
	if strings.ContainsAny(newName, "/\\") {
		s.errorResponse(w, http.StatusBadRequest, "Name cannot contain path separators")
		return
	}

	info, err := os.Stat(oldResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "File or folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access path: "+err.Error())
		return
	}

	if !info.IsDir() && !isMarkdownFile(newName) {
		s.errorResponse(w, http.StatusBadRequest, "File must have .md or .markdown extension")
		return
	}

	parentDir := filepath.Dir(oldResolved)
	newResolved := filepath.Join(parentDir, newName)

	if oldResolved == newResolved {
		s.errorResponse(w, http.StatusBadRequest, "New name is the same as the old name")
		return
	}

	if _, err := os.Stat(newResolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder with this name already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check new path: "+err.Error())
		return
	}

	if err := os.Rename(oldResolved, newResolved); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to rename: "+err.Error())
		return
	}

	newRelPath, err := filepath.Rel(resolvedRoot, newResolved)
	if err != nil {
		newRelPath = newName
	}

	s.jsonResponse(w, http.StatusOK, RenameEntryResponse{
		RootFolder: resolvedRoot,
		OldPath:    filepath.ToSlash(oldNormalized),
		NewPath:    filepath.ToSlash(newRelPath),
	})
}

func (s *Server) handleProjectGitStatus(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	repoPath := strings.TrimSpace(r.URL.Query().Get("repoPath"))
	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, repoPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if !projectHasGitMetadata(targetRepoRoot) {
		s.jsonResponse(w, http.StatusOK, ProjectGitStatusResponse{
			RootFolder: targetRepoRoot,
			HasGit:     false,
			Files:      []ProjectGitChangedFile{},
		})
		return
	}

	porcelainOutput, err := runGitCommand(targetRepoRoot, "status", "--porcelain=v1")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git status: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitStatusResponse{
		RootFolder: targetRepoRoot,
		HasGit:     true,
		Files:      parseGitPorcelain(porcelainOutput),
	})
}

func (s *Server) handleProjectGitCommit(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		s.errorResponse(w, http.StatusBadRequest, "Commit message is required")
		return
	}

	porcelainOutput, err := runGitCommand(targetRepoRoot, "status", "--porcelain=v1")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git status: "+err.Error())
		return
	}
	changedFiles := parseGitPorcelain(porcelainOutput)
	if len(changedFiles) == 0 {
		s.errorResponse(w, http.StatusConflict, "No changed files")
		return
	}
	stagedCount := 0
	for _, file := range changedFiles {
		if file.Staged {
			stagedCount++
		}
	}
	if stagedCount == 0 {
		s.errorResponse(w, http.StatusConflict, "No staged files to commit")
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "commit", "-m", message); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to create commit: "+err.Error())
		return
	}

	commit, err := runGitCommand(targetRepoRoot, "rev-parse", "--short", "HEAD")
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Commit created, but failed to read commit hash: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitResponse{
		RootFolder:     targetRepoRoot,
		Commit:         strings.TrimSpace(commit),
		FilesCommitted: stagedCount,
	})
}

func (s *Server) handleProjectGitStageFile(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "add", "--", normalizedPath); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to stage file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitUnstageFile(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "restore", "--staged", "--", normalizedPath); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to unstage file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) resolveProjectRootFolder(projectID string) (string, error) {
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return "", errors.New("project not found")
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		return "", errors.New("project folder is not configured")
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}

	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		return "", errors.New("project folder path is invalid")
	}

	info, err := os.Stat(resolvedRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("project folder does not exist")
		}
		return "", fmt.Errorf("failed to access project folder: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("project folder path is not a directory")
	}

	return resolvedRoot, nil
}

func projectHasGitMetadata(projectRoot string) bool {
	_, err := os.Stat(filepath.Join(projectRoot, ".git"))
	return err == nil
}

func resolveProjectGitTargetRoot(projectRoot string, repoPath string) (string, error) {
	trimmedPath := strings.TrimSpace(repoPath)
	if trimmedPath == "" {
		return projectRoot, nil
	}

	resolvedPath, _, err := resolveMindPath(projectRoot, trimmedPath)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("target repo path does not exist")
		}
		return "", fmt.Errorf("failed to access target repo path: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("target repo path is not a directory")
	}

	return resolvedPath, nil
}

func runGitCommand(projectRoot string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", projectRoot}, args...)
	cmd := exec.Command("git", commandArgs...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			trimmed = err.Error()
		}
		return "", errors.New(trimmed)
	}
	return trimmed, nil
}

func resolveGitRepoFilePath(repoRoot string, relPath string) (string, error) {
	normalizedPath := filepath.Clean(strings.TrimSpace(relPath))
	if normalizedPath == "" || normalizedPath == "." {
		return "", errors.New("file path is required")
	}
	if filepath.IsAbs(normalizedPath) {
		return "", errors.New("file path must be relative")
	}

	resolvedPath := filepath.Clean(filepath.Join(repoRoot, normalizedPath))
	relToRoot, err := filepath.Rel(repoRoot, resolvedPath)
	if err != nil {
		return "", errors.New("invalid file path")
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return "", errors.New("file path escapes repository root")
	}

	return filepath.ToSlash(relToRoot), nil
}

func parseGitPorcelain(output string) []ProjectGitChangedFile {
	lines := strings.Split(output, "\n")
	files := make([]ProjectGitChangedFile, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || len(line) < 3 {
			continue
		}

		statusCode := line[:2]
		pathPart := strings.TrimSpace(line[3:])
		if strings.Contains(pathPart, " -> ") {
			parts := strings.SplitN(pathPart, " -> ", 2)
			pathPart = strings.TrimSpace(parts[1])
		}
		if pathPart == "" {
			continue
		}

		indexStatus := string(statusCode[0])
		worktreeStatus := string(statusCode[1])
		untracked := statusCode == "??"
		staged := !untracked && indexStatus != " "

		files = append(files, ProjectGitChangedFile{
			Path:           pathPart,
			Status:         strings.TrimSpace(statusCode),
			IndexStatus:    indexStatus,
			WorktreeStatus: worktreeStatus,
			Staged:         staged,
			Untracked:      untracked,
		})
	}
	return files
}
