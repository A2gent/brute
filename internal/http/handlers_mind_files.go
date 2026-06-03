package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func isMarkdownFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
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
