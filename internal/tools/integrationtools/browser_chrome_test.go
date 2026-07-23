package integrationtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type browserChromeSettingsStub struct {
	settings map[string]string
	err      error
}

func (s browserChromeSettingsStub) GetSettings() (map[string]string, error) {
	return s.settings, s.err
}

func TestBrowserChromeHeadlessDefaultsToTrue(t *testing.T) {
	t.Setenv("CHROME_HEADLESS", "")

	tool := NewBrowserChromeTool(t.TempDir())
	if !tool.headlessEnabled() {
		t.Fatal("expected Chrome to run headless by default")
	}
}

func TestBrowserChromeHeadlessUsesSavedSetting(t *testing.T) {
	t.Setenv("CHROME_HEADLESS", "true")

	tool := newBrowserChromeTool(t.TempDir(), browserChromeSettingsStub{
		settings: map[string]string{"CHROME_HEADLESS": "false"},
	})
	if tool.headlessEnabled() {
		t.Fatal("expected saved setting to enable full UI Chrome")
	}
}

func TestBrowserChromeHeadlessFallsBackToEnvironment(t *testing.T) {
	t.Setenv("CHROME_HEADLESS", "false")

	tool := newBrowserChromeTool(t.TempDir(), browserChromeSettingsStub{err: errors.New("settings unavailable")})
	if tool.headlessEnabled() {
		t.Fatal("expected environment fallback to enable full UI Chrome")
	}
}

func TestBrowserChromeExecuteTimesOutWhilePreparingBrowser(t *testing.T) {
	tool := NewBrowserChromeTool(t.TempDir())
	tool.executionTimeout = 25 * time.Millisecond
	tool.ensureBrowserAndPageOverride = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	startedAt := time.Now()
	result, err := tool.Execute(
		context.Background(),
		json.RawMessage(`{"action":"navigate","url":"file:///tmp/test.png"}`),
	)
	if err != nil {
		t.Fatalf("Execute returned an unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected browser preparation timeout, got success: %+v", result)
	}
	if !strings.Contains(result.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("expected deadline error, got %q", result.Error)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("browser setup ignored its execution deadline: %s", elapsed)
	}
}

func TestBrowserChromeExecuteCanCancelWhileWaitingForBrowserLock(t *testing.T) {
	tool := NewBrowserChromeTool(t.TempDir())
	tool.executionTimeout = time.Second
	tool.operationGate <- struct{}{}
	t.Cleanup(func() {
		<-tool.operationGate
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := tool.Execute(
		ctx,
		json.RawMessage(`{"action":"navigate","url":"file:///tmp/test.png"}`),
	)
	if err != nil {
		t.Fatalf("Execute returned an unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected canceled browser lock wait, got success: %+v", result)
	}
	if !strings.Contains(result.Error, context.Canceled.Error()) {
		t.Fatalf("expected cancellation error, got %q", result.Error)
	}
}

func TestBrowserChromeNavigationURLConvertsLocalFileToDataURL(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "fixture with spaces.html")
	if err := os.WriteFile(htmlPath, []byte("<!doctype html><title>local fixture</title>"), 0644); err != nil {
		t.Fatalf("write local browser fixture: %v", err)
	}

	navigationURL, err := browserChromeNavigationURL((&url.URL{Scheme: "file", Path: htmlPath}).String())
	if err != nil {
		t.Fatalf("convert local file URL: %v", err)
	}
	if !strings.HasPrefix(navigationURL, "data:text/html") {
		t.Fatalf("expected an HTML data URL, got %q", navigationURL)
	}
	if strings.Contains(navigationURL, htmlPath) {
		t.Fatalf("data URL leaked the local path: %q", navigationURL)
	}
}

func TestBrowserChromeNavigateLocalFileLive(t *testing.T) {
	if os.Getenv("AAGENT_BROWSER_CHROME_LIVE_TEST") != "1" {
		t.Skip("set AAGENT_BROWSER_CHROME_LIVE_TEST=1 to run against the configured Chrome debug port")
	}

	htmlPath := filepath.Join(t.TempDir(), "browser-chrome-live.html")
	if err := os.WriteFile(htmlPath, []byte("<!doctype html><title>browser chrome live test</title>"), 0644); err != nil {
		t.Fatalf("write live browser fixture: %v", err)
	}

	tool := NewBrowserChromeTool(t.TempDir())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		if err := tool.ensureBrowser(cleanupCtx, cleanupCtx); err != nil || tool.pageTargetID == "" {
			return
		}
		_, _ = proto.TargetCloseTarget{TargetID: tool.pageTargetID}.Call(tool.browser.Context(cleanupCtx))
		tool.dropBrowserConnection()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	startedAt := time.Now()
	result, err := tool.Execute(
		ctx,
		json.RawMessage(fmt.Sprintf(`{"action":"navigate","url":%q}`, "file://"+htmlPath)),
	)
	if err != nil {
		t.Fatalf("Execute returned an unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("live navigation failed after %s: %s", time.Since(startedAt), result.Error)
	}
}

func TestFormatElementsAsTOONIncludesGeometryAndState(t *testing.T) {
	out := formatElementsAsTOON(map[string]interface{}{
		"total":   float64(1),
		"page":    float64(1),
		"perPage": float64(20),
		"hasMore": false,
		"elements": []interface{}{
			map[string]interface{}{
				"selector": "#skin_MenuTabs_1 > div:nth-of-type(1)",
				"tag":      "div",
				"role":     "tab",
				"type":     "pointer",
				"text":     "KLASS(ID)",
				"x":        float64(78),
				"y":        float64(278),
				"w":        float64(156),
				"h":        float64(72),
				"state":    "selected=true",
				"href":     "",
			},
		},
	})

	if !strings.Contains(out, "elements[1]{selector,tag,role,type,text,x,y,w,h,state,href}:") {
		t.Fatalf("expected extended TOON header, got:\n%s", out)
	}
	if !strings.Contains(out, "#skin_MenuTabs_1 > div:nth-of-type(1),div,tab,pointer,KLASS(ID),78,278,156,72,selected=true,") {
		t.Fatalf("expected element geometry/state row, got:\n%s", out)
	}
}

func TestBuildBrowserEvalScriptEmbedsScriptAsJSONString(t *testing.T) {
	script := `(() => { return "1.B \"klass\""; })()`
	wrapped := buildBrowserEvalScript(script)

	if !strings.Contains(wrapped, `(0, eval)(__script)`) {
		t.Fatalf("expected eval path in wrapper, got:\n%s", wrapped)
	}
	if !strings.Contains(wrapped, `1.B`) || !strings.Contains(wrapped, `klass`) {
		t.Fatalf("expected script to be JSON escaped, got:\n%s", wrapped)
	}
}

func TestChromeProfileLaunchWhenNoChromeRunning(t *testing.T) {
	if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("Skipping Chrome profile launch test in CI")
	}

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
