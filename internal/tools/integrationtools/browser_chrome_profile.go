package integrationtools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AgentChromeProfileDirectoryName = "AgentProfile"

// DefaultChromeUserDataDir returns the default Chrome user data directory on macOS.
func DefaultChromeUserDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
}

// AgentChromeProfileDir returns the persistent profile directory used by the agent.
func AgentChromeProfileDir() string {
	base := DefaultChromeUserDataDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, AgentChromeProfileDirectoryName)
}

// AgentChromeDebugUserDataDir returns the dedicated user-data-dir used for remote debugging.
func AgentChromeDebugUserDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Google", "ChromeAgentDebug")
}

// PrepareAgentChromeLaunchLayout ensures profile and debug layout exist:
// - persistent profile directory
// - debug user-data-dir
// - symlink from debug dir to profile directory
// - symlink for Local State so encryption keys are consistent with regular Chrome
func PrepareAgentChromeLaunchLayout(debugUserDataDir, profileDir, profileDirectoryName string) error {
	debugUserDataDir = strings.TrimSpace(debugUserDataDir)
	profileDir = strings.TrimSpace(profileDir)
	profileDirectoryName = strings.TrimSpace(profileDirectoryName)

	if debugUserDataDir == "" {
		return fmt.Errorf("debug user data directory is empty")
	}
	if profileDir == "" {
		return fmt.Errorf("profile directory is empty")
	}
	if profileDirectoryName == "" {
		return fmt.Errorf("profile directory name is empty")
	}

	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("failed to create profile directory: %w", err)
	}
	if err := os.MkdirAll(debugUserDataDir, 0755); err != nil {
		return fmt.Errorf("failed to create debug user data directory: %w", err)
	}

	profileMountPath := filepath.Join(debugUserDataDir, profileDirectoryName)
	if err := ensureSymlink(profileMountPath, profileDir); err != nil {
		return fmt.Errorf("failed to prepare profile symlink: %w", err)
	}

	defaultUserDataDir := DefaultChromeUserDataDir()
	localStateSource := filepath.Join(defaultUserDataDir, "Local State")
	if info, err := os.Stat(localStateSource); err == nil && !info.IsDir() {
		localStateMountPath := filepath.Join(debugUserDataDir, "Local State")
		if err := ensureSymlink(localStateMountPath, localStateSource); err != nil {
			return fmt.Errorf("failed to prepare local state symlink: %w", err)
		}
	}

	return nil
}

func ensureSymlink(path, target string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if existingTarget, readErr := os.Readlink(path); readErr == nil && existingTarget == target {
				return nil
			}
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			return removeErr
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, path)
}
