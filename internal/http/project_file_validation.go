package http

import (
	"bytes"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxProjectEditableFileBytes = 512 * 1024

const maxProjectEditableFileLines = 20000

func isPDFFile(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".pdf")
}

func isProjectRawPreviewFile(name string) bool {
	return isPDFFile(name) || isProjectBrowserImageFile(name)
}

func isProjectBrowserImageFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".apng", ".avif", ".bmp", ".gif", ".ico", ".jpeg", ".jpg", ".png", ".svg", ".webp":
		return true
	default:
		return false
	}
}

func projectRawPreviewContentType(name string) string {
	if isPDFFile(name) {
		return "application/pdf"
	}
	if contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

func isProjectEditableFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return !isProjectBlockedMediaFileExtension(ext)
}

func isProjectBlockedMediaFileExtension(ext string) bool {
	switch ext {
	case ".avif", ".bmp", ".gif", ".heic", ".heif", ".ico", ".jpeg", ".jpg", ".png", ".svg", ".svgz", ".tif", ".tiff", ".webp":
		return true
	case ".3g2", ".3gp", ".avi", ".flv", ".m4v", ".mkv", ".mov", ".mp4", ".mpeg", ".mpg", ".ogv", ".webm", ".wmv":
		return true
	default:
		return false
	}
}

func validateProjectFileContent(content []byte, action string) error {
	if len(content) > maxProjectEditableFileBytes {
		return fmt.Errorf("File is too large to %s (max 512 KiB)", action)
	}
	if bytes.Contains(content, []byte{0}) || !utf8.Valid(content) {
		return fmt.Errorf("File must be UTF-8 text to %s", action)
	}
	if countProjectFileLines(content) > maxProjectEditableFileLines {
		return fmt.Errorf("File has too many lines to %s (max 20,000 lines)", action)
	}
	return nil
}

func countProjectFileLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lineCount := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lineCount++
	}
	return lineCount
}
