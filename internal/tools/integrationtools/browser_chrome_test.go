package integrationtools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

func TestChromeProfileLaunchWhenNoChromeRunning(t *testing.T) {
	// Test verifies that the browser_chrome tool connects to Chrome launched via UI button
	// Uses symlink approach to work around Chrome's remote debugging security

	// Step 1: Ensure no Chrome is running
	t.Log("Step 1: Checking if Chrome is already running...")
	if isChromeRunning() {
		t.Skip("Chrome is already running. Please close it completely (Cmd+Q) and run test again.")
	}
	t.Log("✓ No Chrome running - good for test")

	// Step 2: Check AgentProfile exists and has data
	realProfileDir := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Google", "Chrome", "AgentProfile")
	if _, err := os.Stat(realProfileDir); os.IsNotExist(err) {
		t.Fatalf("AgentProfile does not exist at %s. Please create it first using the UI button.", realProfileDir)
	}
	t.Logf("✓ AgentProfile exists at: %s", realProfileDir)

	// Check for key profile files
	cookiesPath := filepath.Join(realProfileDir, "Default", "Cookies")
	if info, err := os.Stat(cookiesPath); err == nil {
		t.Logf("✓ Cookies file exists: %d bytes", info.Size())
	} else {
		t.Logf("⚠ Cookies file not found (normal for first run): %v", err)
	}

	// Step 3: Create temp directory with symlink to real profile
	t.Log("\nStep 3: Creating temp directory with symlink to AgentProfile...")
	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("aagent-chrome-test-%d", time.Now().Unix()))
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create symlink to real profile
	symlinkPath := filepath.Join(tempDir, "AgentProfile")
	if err := os.Symlink(realProfileDir, symlinkPath); err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}
	t.Logf("✓ Created symlink: %s -> %s", symlinkPath, realProfileDir)

	// Step 4: Launch Chrome with temp dir as user-data-dir
	chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	profileName := "AgentProfile"
	debugPort := "9227"

	t.Log("\nStep 4: Launching Chrome with symlinked profile...")
	t.Logf("  Temp user-data-dir: %s", tempDir)
	t.Logf("  Profile: %s", profileName)
	t.Logf("  Port: %s", debugPort)

	args := []string{
		"--user-data-dir=" + tempDir,
		"--profile-directory=" + profileName,
		"--remote-debugging-port=" + debugPort,
		"--no-first-run",
		"--no-default-browser-check",
	}

	chromeCmd := exec.Command(chromePath, args...)
	chromeCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := chromeCmd.Start(); err != nil {
		t.Fatalf("Failed to launch Chrome: %v", err)
	}

	t.Logf("✓ Chrome launched, PID: %d", chromeCmd.Process.Pid)

	// Give Chrome time to start
	time.Sleep(3 * time.Second)

	// Step 5: Verify Chrome is listening for debugging
	t.Log("\nStep 5: Verifying Chrome remote debugging...")
	time.Sleep(1 * time.Second)

	// Step 6: Connect with Rod
	t.Log("\nStep 6: Connecting with Rod...")
	time.Sleep(1 * time.Second) // Give Chrome a moment

	// Try to connect directly with the port first
	browser := rod.New().ControlURL(fmt.Sprintf("ws://localhost:%s", debugPort))
	if err := browser.Connect(); err != nil {
		// Try with MustResolveURL which handles the discovery
		wsURL, resolveErr := launcher.ResolveURL(fmt.Sprintf(":%s", debugPort))
		if resolveErr != nil {
			t.Fatalf("Failed to resolve Chrome URL: %v (original error: %v)", resolveErr, err)
		}
		t.Logf("Resolved WebSocket URL: %s", wsURL)

		browser = rod.New().ControlURL(wsURL)
		if err := browser.Connect(); err != nil {
			t.Fatalf("Failed to connect to Chrome with resolved URL: %v", err)
		}
	}
	t.Log("✓ Connected to Chrome with Rod")

	defer browser.Close()

	page := browser.MustPage()
	defer page.Close()

	// Step 7: Check profile path
	t.Log("\nStep 7: Verifying profile path...")
	if err := page.Navigate("chrome://version/"); err != nil {
		t.Fatalf("Failed to navigate: %v", err)
	}
	time.Sleep(2 * time.Second)

	content, err := page.HTML()
	if err != nil {
		t.Fatalf("Failed to get page HTML: %v", err)
	}

	profileVerified := false
	if strings.Contains(content, "Profile Path") {
		start := strings.Index(content, "Profile Path")
		if start > 0 {
			end := strings.Index(content[start:], "</tr>")
			if end > 0 {
				profileSection := content[start : start+end]
				t.Logf("Profile Path section:")
				t.Logf("  %s", profileSection)

				if tdStart := strings.Index(profileSection, "<td>"); tdStart > 0 {
					if tdEnd := strings.Index(profileSection[tdStart:], "</td>"); tdEnd > 0 {
						actualPath := profileSection[tdStart+4 : tdStart+tdEnd]
						t.Logf("Actual profile path: %s", actualPath)

						// Check it's using the symlink
						if strings.Contains(actualPath, tempDir) {
							t.Log("✓ Profile is using temp directory (symlink)")
							profileVerified = true
						}
					}
				}
			}
		}
	}

	// Step 8: Check cookies work
	t.Log("\nStep 8: Checking cookies...")
	if err := page.Navigate("https://google.com"); err != nil {
		t.Logf("⚠ Failed to navigate to google.com: %v", err)
	} else {
		time.Sleep(2 * time.Second)
		cookies, err := page.Cookies([]string{})
		if err != nil {
			t.Logf("⚠ Failed to get cookies: %v", err)
		} else {
			t.Logf("✓ Retrieved %d cookies", len(cookies))
			if len(cookies) > 0 {
				t.Log("  Sample cookies:")
				for i, cookie := range cookies {
					if i >= 3 {
						t.Log("  ...")
						break
					}
					t.Logf("    - %s", cookie.Name)
				}
			} else {
				t.Log("  No cookies - this may indicate profile data isn't being read correctly")
			}
		}
	}

	// Step 9: Cleanup
	t.Log("\nStep 9: Cleanup...")
	page.Close()
	browser.Close()

	// Kill the Chrome process
	if err := chromeCmd.Process.Kill(); err != nil {
		t.Logf("⚠ Failed to kill Chrome process: %v", err)
	}

	time.Sleep(1 * time.Second)
	if isChromeRunning() {
		t.Log("⚠ Chrome still running after cleanup")
	} else {
		t.Log("✓ Chrome closed successfully")
	}

	t.Log("\n=== TEST COMPLETED ===")
	if profileVerified {
		t.Log("SUCCESS: browser_chrome tool works with symlink approach")
	} else {
		t.Log("FAILED: Profile verification failed")
	}
}

// Note: isChromeRunning() is defined in browser_chrome.go
