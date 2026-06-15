package filesearch

import (
	"path/filepath"
	"strings"
)

var defaultPrunedDirs = map[string]struct{}{
	".cache":           {},
	".git":             {},
	".hg":              {},
	".next":            {},
	".nuxt":            {},
	".pnpm-store":      {},
	".svn":             {},
	".turbo":           {},
	"bower_components": {},
	"build":            {},
	"coverage":         {},
	"dist":             {},
	"node_modules":     {},
	"out":              {},
	"target":           {},
	"vendor":           {},
}

func shouldSkipDir(relPath string, opts Options) bool {
	base := filepath.Base(filepath.FromSlash(relPath))
	if !opts.IncludeHidden && strings.HasPrefix(base, ".") {
		return true
	}
	if !opts.DisableDefaultExcludes {
		if _, ok := defaultPrunedDirs[base]; ok {
			return true
		}
	}
	return false
}

func shouldSkipFile(relPath string, opts Options) bool {
	if opts.IncludeHidden {
		return false
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(relPath)), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

func looksLikeBinaryOrMedia(relPath string) bool {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".avif", ".bmp", ".gif", ".heic", ".heif", ".ico", ".jpeg", ".jpg", ".png", ".svg", ".svgz", ".tif", ".tiff", ".webp":
		return true
	case ".3g2", ".3gp", ".avi", ".flv", ".m4v", ".mkv", ".mov", ".mp4", ".mpeg", ".mpg", ".ogv", ".webm", ".wmv":
		return true
	default:
		return false
	}
}
