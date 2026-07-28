package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
	s.warmProjectSearchIndex(project, resolvedRoot)

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveProjectPath(resolvedRoot, relPath)
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
	resolvedPath, normalizedRelPath, err := resolveProjectPath(resolvedRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isProjectEditableFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Images and videos cannot be opened in the project editor")
		return
	}
	if isPDFFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "PDF files can be opened in the project preview")
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
	if info.Size() > maxProjectEditableFileBytes {
		s.errorResponse(w, http.StatusBadRequest, "File is too large to open (max 512 KiB)")
		return
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to read file: "+err.Error())
		return
	}
	if err := validateProjectFileContent(content, "open"); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileResponse{
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    string(content),
	})
}

// handleGetProjectFileRaw streams a file from a project's folder for browser-native previews.
func (s *Server) handleGetProjectFileRaw(w http.ResponseWriter, r *http.Request) {
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
	resolvedPath, normalizedRelPath, err := resolveProjectPath(resolvedRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isProjectRawPreviewFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Only PDF and image files can be previewed")
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

	w.Header().Set("Content-Type", projectRawPreviewContentType(normalizedRelPath))
	w.Header().Set("Content-Disposition", "inline; filename=\""+strings.ReplaceAll(filepath.Base(normalizedRelPath), "\"", "")+"\"")
	http.ServeFile(w, r, resolvedPath)
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

	resolvedPath, normalizedRelPath, err := resolveProjectPath(resolvedRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isProjectEditableFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Images and videos cannot be created or edited in the project editor")
		return
	}
	if err := validateProjectFileContent([]byte(req.Content), "create or edit"); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	parentDir := filepath.Dir(resolvedPath)
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(parentDir, 0o755); err != nil {
				s.errorResponse(w, http.StatusInternalServerError, "Failed to create parent folder: "+err.Error())
				return
			}
			parentInfo, err = os.Stat(parentDir)
			if err != nil {
				s.errorResponse(w, http.StatusInternalServerError, "Failed to access parent folder: "+err.Error())
				return
			}
		} else {
			s.errorResponse(w, http.StatusBadRequest, "Failed to access parent folder: "+err.Error())
			return
		}
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
	invalidateProjectSearchIndex(resolvedRoot)

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
	resolvedPath, normalizedRelPath, err := resolveProjectPath(resolvedRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
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
	invalidateProjectSearchIndex(resolvedRoot)

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

	fromResolved, fromNormalized, err := resolveProjectPath(resolvedRoot, req.FromPath)
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

	toResolved, toNormalized, err := resolveProjectPath(resolvedRoot, req.ToPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid destination path: "+err.Error())
		return
	}
	if toNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Destination path is required")
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
	invalidateProjectSearchIndex(resolvedRoot)

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

	resolvedPath, normalizedRelPath, err := resolveProjectPath(resolvedRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "Folder path is required")
		return
	}

	if stat, err := os.Stat(resolvedPath); err == nil {
		if !stat.IsDir() {
			s.errorResponse(w, http.StatusConflict, "A file or folder already exists at this path")
			return
		}
		// Folder already exists (e.g. editing an agent definition). Treat as success.
		s.jsonResponse(w, http.StatusOK, CreateFolderResponse{
			RootFolder: resolvedRoot,
			Path:       filepath.ToSlash(normalizedRelPath),
		})
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check path: "+err.Error())
		return
	}

	if err := os.MkdirAll(resolvedPath, 0o755); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create folder: "+err.Error())
		return
	}
	invalidateProjectSearchIndex(resolvedRoot)

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

	oldResolved, oldNormalized, err := resolveProjectPath(resolvedRoot, req.OldPath)
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

	if !info.IsDir() && !isProjectEditableFile(newName) {
		s.errorResponse(w, http.StatusBadRequest, "File cannot be renamed to an image or video extension")
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
	invalidateProjectSearchIndex(resolvedRoot)

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
