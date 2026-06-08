package skills

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestLoadSkillsFromDirectoryIgnoresNestedProjectMarkdown(t *testing.T) {
	root := t.TempDir()
	writeSkillTestFile(t, filepath.Join(root, "AGENTS.md"), "# Root Agent\n\nRoot guidance.")
	writeSkillTestFile(t, filepath.Join(root, "notes.markdown"), "# Notes Skill\n\nPlain root markdown is still a skill.")
	writeSkillTestFile(t, filepath.Join(root, "installed", "SKILL.md"), "---\nname: Installed Skill\ndescription: Registry-style skill package\n---\nUse installed skill.")
	writeSkillTestFile(t, filepath.Join(root, "installed", "README.md"), "# Installed package docs\n")
	writeSkillTestFile(t, filepath.Join(root, "speech", "whisper", "source", "whisper.cpp", "README.md"), "# whisper.cpp\n")
	writeSkillTestFile(t, filepath.Join(root, "category", "nested", "SKILL.md"), "# Too Deep\n")
	writeSkillTestFile(t, filepath.Join(root, ".hidden", "SKILL.md"), "# Hidden\n")

	loaded, err := LoadSkillsFromDirectory(root, DefaultConfig())
	if err != nil {
		t.Fatalf("LoadSkillsFromDirectory() error = %v", err)
	}

	got := make([]string, 0, len(loaded))
	for _, skill := range loaded {
		got = append(got, skill.RelativePath)
	}
	sort.Strings(got)

	want := []string{"AGENTS.md", "installed/SKILL.md", "notes.markdown"}
	if !equalStringSlices(got, want) {
		t.Fatalf("loaded skill paths = %#v, want %#v", got, want)
	}
}

func writeSkillTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
