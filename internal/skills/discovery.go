package skills

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// WalkDiscoverableSkillFiles visits only files that are intended to be external skills.
// Top-level markdown files support simple personal skill folders, while one-level
// <skill>/SKILL.md packages support registry installs without crawling vendored repos.
func WalkDiscoverableSkillFiles(rootDir string, visit func(path string, relativePath string) error) error {
	rootDir = filepath.Clean(rootDir)
	return filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			if shouldSkipSkillDiscoveryDir(rootDir, path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		relativePath, ok := discoverableSkillRelativePath(rootDir, path)
		if !ok {
			return nil
		}
		return visit(path, relativePath)
	})
}

func shouldSkipSkillDiscoveryDir(rootDir string, path string, name string) bool {
	if filepath.Clean(path) == rootDir {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}

	relativePath, err := filepath.Rel(rootDir, path)
	if err != nil {
		return true
	}

	// Discovery intentionally descends only one directory level. That keeps
	// registry-style packages visible but prevents arbitrary source trees under
	// the skills folder (for example whisper.cpp) from becoming hundreds of skills.
	return len(splitRelativePath(relativePath)) >= 2
}

func DiscoverableSkillRelativePath(rootDir string, path string) (string, bool) {
	return discoverableSkillRelativePath(filepath.Clean(rootDir), path)
}

func IsPackagedSkillManifest(rootDir string, path string) bool {
	relativePath, ok := DiscoverableSkillRelativePath(rootDir, path)
	if !ok {
		return false
	}
	parts := splitRelativePath(relativePath)
	return len(parts) == 2 && isSkillManifestFileName(parts[1])
}

func discoverableSkillRelativePath(rootDir string, path string) (string, bool) {
	relativePath, err := filepath.Rel(rootDir, path)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(relativePath) {
		return "", false
	}

	parts := splitRelativePath(relativePath)
	if len(parts) == 1 {
		if isMarkdownSkillFileName(parts[0]) {
			return filepath.ToSlash(filepath.Clean(relativePath)), true
		}
		return "", false
	}

	if len(parts) == 2 && isSkillManifestFileName(parts[1]) {
		return filepath.ToSlash(filepath.Clean(relativePath)), true
	}
	return "", false
}

func splitRelativePath(relativePath string) []string {
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." || cleaned == "" {
		return nil
	}
	return strings.Split(filepath.ToSlash(cleaned), "/")
}

func isMarkdownSkillFileName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

func isSkillManifestFileName(name string) bool {
	lower := strings.ToLower(name)
	return lower == "skill.md" || lower == "skill.markdown"
}
