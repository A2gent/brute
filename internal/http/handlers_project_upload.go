package http

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxProjectUploadFileBytes  = 512 << 20
	maxProjectUploadTotalBytes = 1 << 30
	maxProjectUploadFormMemory = 8 << 20
)

// UploadProjectFilesResponse lists the project-relative paths written by an upload.
type UploadProjectFilesResponse struct {
	RootFolder    string   `json:"root_folder"`
	FolderPath    string   `json:"folder_path"`
	UploadedFiles []string `json:"uploaded_files"`
}

// handleUploadProjectFiles copies uploaded bytes into a project folder.
// WHY: desktop webviews hand OS drops to the page as File blobs without filesystem
// paths, so the host cannot move anything; the browser sends content instead.
func (s *Server) handleUploadProjectFiles(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "project not found") {
			status = http.StatusNotFound
		}
		s.errorResponse(w, status, err.Error())
		return
	}

	targetDir, normalizedFolderPath, err := resolveProjectPath(resolvedRoot, r.URL.Query().Get("path"))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	targetInfo, err := os.Stat(targetDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Target folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access target folder: "+err.Error())
		return
	}
	if !targetInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Target path is not a folder")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxProjectUploadTotalBytes)
	if err := r.ParseMultipartForm(maxProjectUploadFormMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.errorResponse(w, http.StatusRequestEntityTooLarge, "Upload is too large")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Invalid multipart request: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	headers := r.MultipartForm.File["files"]
	if len(headers) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "No files were uploaded")
		return
	}

	// WHY: validate every target first so a bad name or collision cannot leave a half-applied drop.
	destinations := make([]string, 0, len(headers))
	names := make([]string, 0, len(headers))
	for _, header := range headers {
		name, nameErr := sanitizeProjectUploadName(header.Filename)
		if nameErr != nil {
			s.errorResponse(w, http.StatusBadRequest, nameErr.Error())
			return
		}
		destination := filepath.Join(targetDir, name)
		if _, statErr := os.Stat(destination); statErr == nil {
			s.errorResponse(w, http.StatusConflict, fmt.Sprintf("An item named %q already exists in the target folder", name))
			return
		} else if !errors.Is(statErr, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Failed to check target item: "+statErr.Error())
			return
		}
		if header.Size > maxProjectUploadFileBytes {
			s.errorResponse(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("File %q is too large (max 512 MiB)", name))
			return
		}
		names = append(names, name)
		destinations = append(destinations, destination)
	}

	uploadedPaths := make([]string, 0, len(destinations))
	for index, destination := range destinations {
		if writeErr := writeUploadedProjectFile(headers[index], destination); writeErr != nil {
			s.errorResponse(w, http.StatusBadRequest, writeErr.Error())
			return
		}
		relativePath, relErr := filepath.Rel(resolvedRoot, destination)
		if relErr != nil {
			relativePath = names[index]
		}
		uploadedPaths = append(uploadedPaths, filepath.ToSlash(relativePath))
	}
	invalidateProjectSearchIndex(resolvedRoot)

	s.jsonResponse(w, http.StatusOK, UploadProjectFilesResponse{
		RootFolder:    resolvedRoot,
		FolderPath:    filepath.ToSlash(normalizedFolderPath),
		UploadedFiles: uploadedPaths,
	})
}

// sanitizeProjectUploadName reduces a client filename to a single safe path segment.
func sanitizeProjectUploadName(raw string) (string, error) {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "", errors.New("Uploaded file has no valid name")
	}
	if strings.ContainsRune(name, 0) {
		return "", errors.New("Uploaded file name is invalid")
	}
	return name, nil
}

func writeUploadedProjectFile(header *multipart.FileHeader, destination string) error {
	source, err := header.Open()
	if err != nil {
		return fmt.Errorf("Failed to read uploaded file %q: %w", header.Filename, err)
	}
	defer source.Close()

	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("Failed to create uploaded file: %w", err)
	}

	written, copyErr := io.Copy(destinationFile, io.LimitReader(source, maxProjectUploadFileBytes+1))
	closeErr := destinationFile.Close()
	if written > maxProjectUploadFileBytes {
		_ = os.Remove(destination)
		return fmt.Errorf("File %q is too large (max 512 MiB)", filepath.Base(destination))
	}
	if copyErr != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("Failed to write uploaded file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("Failed to write uploaded file: %w", closeErr)
	}
	return nil
}
