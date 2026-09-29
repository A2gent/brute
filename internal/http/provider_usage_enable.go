package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/config"
	"github.com/go-chi/chi/v5"
)

// Claude Code only exposes subscriber usage in statusLine input. Install its collector
// on explicit request rather than accessing private credentials or replacing user scripts.
func (s *Server) handleEnableClaudeUsage(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "providerType") != "anthropic" {
		s.errorResponse(w, http.StatusBadRequest, "Automatic usage setup is available only for the default Claude instance")
		return
	}
	if !s.providerConfiguredForUse(config.ProviderAnthropic) {
		s.errorResponse(w, http.StatusBadRequest, "Claude Code CLI is not configured")
		return
	}
	if s.config.Providers[string(config.ProviderAnthropic)].ClaudeConfigDir != "" {
		s.errorResponse(w, http.StatusConflict, "Claude has a custom config directory; configure its statusLine manually")
		return
	}
	path, err := claudeRateLimitsCachePath()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Cannot locate Claude Code settings")
		return
	}
	configDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if configDir == "" {
		configDir = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(configDir) {
		s.errorResponse(w, http.StatusBadRequest, "CLAUDE_CONFIG_DIR must be absolute")
		return
	}
	settingsPath := filepath.Join(configDir, "settings.json")
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		s.errorResponse(w, http.StatusInternalServerError, "Cannot read Claude Code settings: "+err.Error())
		return
	}
	if len(data) > 0 && json.Unmarshal(data, &settings) != nil {
		s.errorResponse(w, http.StatusConflict, "Claude Code settings are not valid JSON; no changes were made")
		return
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}
	scriptPath := filepath.Join(configDir, "a2gent-usage-statusline.py")
	command := "python3 " + quoteClaudeUsagePath(scriptPath)
	if existing := settings["statusLine"]; len(existing) > 0 && string(existing) != "null" {
		var statusLine struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(existing, &statusLine) != nil || statusLine.Command != command {
			s.errorResponse(w, http.StatusConflict, "Claude Code already has a statusLine. Configure it to write rate_limits to the cache manually; A2gent will not replace it.")
			return
		}
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Cannot create Claude Code config directory: "+err.Error())
		return
	}
	cacheJSON, _ := json.Marshal(path)
	script := fmt.Sprintf(`import json
import os
import pathlib
import sys
import tempfile

cache = pathlib.Path(%s)
payload = json.load(sys.stdin)
limits = payload.get("rate_limits") or {}
if limits.get("five_hour") or limits.get("seven_day"):
    cache.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", dir=cache.parent, delete=False) as tmp:
        json.dump({"rate_limits": limits}, tmp)
        tmp.write("\n")
        name = tmp.name
    os.replace(name, cache)
print("cc")
`, cacheJSON)
	if err := installClaudeUsageScript(scriptPath, []byte(script)); err != nil {
		s.errorResponse(w, http.StatusConflict, "Cannot install Claude usage collector: "+err.Error())
		return
	}
	statusLine, _ := json.Marshal(map[string]string{"type": "command", "command": command})
	settings["statusLine"] = statusLine
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err == nil {
		var mode os.FileMode = 0600
		if info, statErr := os.Stat(settingsPath); statErr == nil {
			mode = info.Mode().Perm()
		}
		err = writeClaudeUsageSettings(settingsPath, append(encoded, '\n'), mode)
	}
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Cannot save Claude Code settings: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"message": "Claude usage collection enabled. Send a message with Claude Code, then refresh this card to see remaining usage."})
}

func installClaudeUsageScript(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		// Only replace an existing collector we recognize; never overwrite user scripts.
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !strings.Contains(string(current), "cache = pathlib.Path(") || !strings.Contains(string(current), "rate_limits") {
			return fmt.Errorf("a different script already exists at %s", path)
		}
		return os.WriteFile(path, data, 0600)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}

func writeClaudeUsageSettings(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".a2gent-settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func quoteClaudeUsagePath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}
