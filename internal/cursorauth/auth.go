package cursorauth

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/A2gent/brute/internal/config"
)

const (
	macOSKeychainService = "cursor-access-token"
	macOSKeychainAccount = "cursor-user"
	cursorAccessTokenEnv          = "AAGENT_CURSOR_ACCESS_TOKEN"
	cursorSkipPlatformAuthEnv     = "AAGENT_CURSOR_SKIP_PLATFORM_AUTH"
)

type cursorAuthFile struct {
	AccessToken string `json:"accessToken"`
}

// DefaultPath returns the first Cursor Agent CLI auth.json path that exists.
func DefaultPath() string {
	for _, path := range AuthFileCandidates() {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	candidates := AuthFileCandidates()
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

// AuthFileCandidates returns likely Cursor Agent CLI auth.json locations.
func AuthFileCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{
		filepath.Join(home, ".cursor-agent", "auth.json"),
		filepath.Join(home, ".cursor", "auth.json"),
	}
	if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
		candidates = append(candidates,
			filepath.Join(configHome, "cursor-agent", "auth.json"),
			filepath.Join(configHome, "cursor", "auth.json"),
		)
	} else {
		candidates = append(candidates,
			filepath.Join(home, ".config", "cursor-agent", "auth.json"),
			filepath.Join(home, ".config", "cursor", "auth.json"),
		)
	}
	return candidates
}

// Load reads Cursor OAuth credentials from an explicit auth file, env override, macOS keychain, or auth.json files.
func Load(path string) (*config.OAuthConfig, string, error) {
	if authPath := strings.TrimSpace(path); authPath != "" {
		authPath = expandHome(authPath)
		token, err := resolveAccessTokenFromAuthFile(authPath)
		if err != nil {
			return nil, authPath, err
		}
		return &config.OAuthConfig{AccessToken: token}, authPath, nil
	}

	if token := strings.TrimSpace(os.Getenv(cursorAccessTokenEnv)); token != "" {
		return &config.OAuthConfig{AccessToken: token}, cursorAccessTokenEnv, nil
	}

	if token, err := resolveAccessTokenFromPlatform(); err == nil && token != "" {
		return &config.OAuthConfig{AccessToken: token}, "macOS keychain (cursor-access-token)", nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}

	authPath := strings.TrimSpace(path)
	if authPath == "" {
		for _, candidate := range AuthFileCandidates() {
			token, err := resolveAccessTokenFromAuthFile(candidate)
			if err == nil && token != "" {
				return &config.OAuthConfig{AccessToken: token}, candidate, nil
			}
			if err != nil && !os.IsNotExist(err) {
				return nil, candidate, err
			}
		}
		return nil, DefaultPath(), fmt.Errorf("Cursor Agent CLI access token not found; run `agent login` or import OAuth from the Cursor provider page")
	}

	authPath = expandHome(authPath)
	token, err := resolveAccessTokenFromAuthFile(authPath)
	if err != nil {
		return nil, authPath, err
	}
	return &config.OAuthConfig{AccessToken: token}, authPath, nil
}

func resolveAccessTokenFromPlatform() (string, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(cursorSkipPlatformAuthEnv)), "true") {
		return "", os.ErrNotExist
	}
	if runtime.GOOS != "darwin" {
		return "", os.ErrNotExist
	}
	cmd := exec.Command("security", "find-generic-password", "-s", macOSKeychainService, "-a", macOSKeychainAccount, "-w")
	out, err := cmd.Output()
	if err != nil {
		return "", os.ErrNotExist
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", os.ErrNotExist
	}
	return token, nil
}

func resolveAccessTokenFromAuthFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var payload cursorAuthFile
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("failed to parse Cursor auth file at %s: %w", path, err)
	}
	token := strings.TrimSpace(payload.AccessToken)
	if token == "" {
		return "", fmt.Errorf("Cursor auth file at %s does not contain accessToken", path)
	}
	return token, nil
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
