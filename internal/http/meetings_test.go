package http

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestResolveMeetingAudioPathPrefersProjectResolvedFolder(t *testing.T) {
	workDir := t.TempDir()
	projectDir := filepath.Join(workDir, "project")
	audioDir := filepath.Join(projectDir, "meetings", "audio")
	if err := os.MkdirAll(audioDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	audioPath := filepath.Join(audioDir, "2026-07-17_10-00-00-meeting-me.webm")
	if err := os.WriteFile(audioPath, []byte("fake-audio"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stalePath := filepath.Join(workDir, "meetings", "audio", "2026-07-17_10-00-00-meeting-me.webm")
	server := &Server{config: &config.Config{WorkDir: workDir}}

	resolved, err := server.resolveMeetingAudioPath(filepath.Join("project", "meetings", "audio", "2026-07-17_10-00-00-meeting-me.webm"))
	if err != nil {
		t.Fatalf("resolveMeetingAudioPath() error = %v", err)
	}
	if resolved != audioPath {
		t.Fatalf("resolveMeetingAudioPath() = %q, want %q", resolved, audioPath)
	}

	filtered := filterExistingMeetingAudioPaths(server, []string{stalePath, audioPath})
	if len(filtered) != 1 || filtered[0] != audioPath {
		t.Fatalf("filterExistingMeetingAudioPaths() = %#v, want [%q]", filtered, audioPath)
	}
}
