package http

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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

func directoryHasChildren(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) > 0
}
