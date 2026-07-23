package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	browserChromeSetupAttempts       = 2
	browserChromeSetupAttemptTimeout = 10 * time.Second
	browserChromeSetupRetryDelay     = 250 * time.Millisecond
	browserChromeLaunchWait          = 3 * time.Second
)

// isChromeRunning reports whether any Google Chrome process is currently running.
// Integration tests use this to decide whether they can safely launch a throwaway instance.
func isChromeRunning() bool {
	cmd := exec.Command("pgrep", "-x", "Google Chrome")
	err := cmd.Run()
	return err == nil
}

func (t *BrowserChromeTool) ensureBrowser(probeCtx, connectionCtx context.Context) error {
	// Check if existing browser connection is still valid
	if t.browser != nil {
		// Try to verify the connection is still alive
		_, err := t.browser.Context(probeCtx).Version()
		if err == nil {
			return nil
		}
		// Connection is dead, reset it
		logging.Info("Browser connection is stale, reconnecting...")
		t.dropBrowserConnection()
	}

	logging.Info("Connecting to Chrome on port %s...", t.debugPort)

	// Resolve through Chrome's version endpoint with the operation context.
	resolvedURL, resolveErr := resolveChromeWebSocketURL(probeCtx, t.debugPort)
	if resolveErr == nil {
		browser := rod.New().Context(connectionCtx).ControlURL(resolvedURL)
		if err := browser.Connect(); err == nil {
			logging.Info("Connected to Chrome via resolved URL: %s", resolvedURL)
			t.browser = browser
			return nil
		}
	}
	if err := probeCtx.Err(); err != nil {
		return err
	}

	logging.Info("No debuggable Chrome instance found on port %s, launching agent Chrome...", t.debugPort)

	// Prepare launch layout:
	// - persistent profile in default Chrome directory (switchable in regular Chrome)
	// - separate debug user-data-dir to allow remote debugging.
	if err := PrepareAgentChromeLaunchLayout(t.userDataDir, t.profileDir, t.profileDirectory); err != nil {
		return err
	}

	// Launch Chrome with dedicated debug user-data-dir and explicit profile directory.
	chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

	logging.Info("Launching Chrome with user-data-dir: %s", t.userDataDir)
	logging.Info("Using Chrome profile directory: %s (path: %s)", t.profileDirectory, t.profileDir)
	headless := t.headlessEnabled()
	logging.Info("Headless mode: %v", headless)

	args := []string{
		"--user-data-dir=" + t.userDataDir,
		"--profile-directory=" + t.profileDirectory,
		"--remote-debugging-port=" + t.debugPort,
		"--remote-debugging-address=127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
		"--new-window",
	}

	if headless {
		args = append(args, "--headless=new")
		logging.Info("Running in headless mode")
	}

	cmd := exec.Command(chromePath, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch Chrome: %w", err)
	}

	logging.Info("Chrome launched, PID: %d", cmd.Process.Pid)

	// Wait for Chrome to start without making cancellation wait for a fixed sleep.
	if err := waitForBrowserChromeRetry(probeCtx, browserChromeLaunchWait); err != nil {
		_ = cmd.Process.Kill()
		return err
	}

	// Connect to Chrome
	resolvedURL, resolveErr = resolveChromeWebSocketURL(probeCtx, t.debugPort)
	if resolveErr != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("failed to resolve Chrome URL: %w", resolveErr)
	}
	logging.Info("Resolved WebSocket URL: %s", resolvedURL)

	browser := rod.New().Context(connectionCtx).ControlURL(resolvedURL)
	if err := browser.Connect(); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("failed to connect to Chrome: %w", err)
	}

	logging.Info("Connected to Chrome successfully")
	t.browser = browser
	return nil
}

// ensureBrowserAndPage ensures both browser connection and a persistent page exist
func (t *BrowserChromeTool) ensureBrowserAndPage(ctx context.Context) error {
	var lastErr error
	for attempt := 1; attempt <= browserChromeSetupAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		tools.ReportProgress(ctx, tools.ProgressEvent{
			Status:  "running",
			Content: fmt.Sprintf("Preparing Chrome (attempt %d/%d)", attempt, browserChromeSetupAttempts),
		})

		attemptCtx, cancelAttempt := context.WithTimeout(ctx, browserChromeSetupAttemptTimeout)
		err := t.ensureBrowserAndPageAttempt(attemptCtx, ctx)
		if err == nil {
			cancelAttempt()
			return nil
		}
		cancelAttempt()
		lastErr = err
		logging.Warn("Chrome setup attempt %d/%d failed: %v", attempt, browserChromeSetupAttempts, err)
		t.dropBrowserConnection()

		if attempt < browserChromeSetupAttempts {
			if err := waitForBrowserChromeRetry(ctx, browserChromeSetupRetryDelay); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("Chrome setup failed after %d attempts: %w", browserChromeSetupAttempts, lastErr)
}

func (t *BrowserChromeTool) ensureBrowserAndPageAttempt(probeCtx, connectionCtx context.Context) error {
	if err := t.ensureBrowser(probeCtx, connectionCtx); err != nil {
		return err
	}

	if t.pageTargetID != "" {
		if _, err := (proto.TargetGetTargetInfo{TargetID: t.pageTargetID}).Call(t.browser.Context(probeCtx)); err == nil {
			if _, err := t.browser.PageFromTarget(t.pageTargetID); err == nil {
				return nil
			}
		}
		logging.Info("Page connection is stale, creating new page...")
		t.pageTargetID = ""
	}

	// Creating a fresh target avoids Target.getTargets, the call that previously
	// left sessions blocked before their action timeout was installed.
	logging.Info("Creating new browser page...")
	target, err := (proto.TargetCreateTarget{URL: "about:blank"}).Call(t.browser.Context(probeCtx))
	if err != nil {
		return fmt.Errorf("failed to create browser page: %w", err)
	}
	t.pageTargetID = target.TargetID
	if _, err := t.browser.PageFromTarget(t.pageTargetID); err != nil {
		t.pageTargetID = ""
		return fmt.Errorf("failed to attach browser page: %w", err)
	}
	logging.Info("Browser page created successfully")
	return nil
}

func (t *BrowserChromeTool) pageForContext(ctx context.Context) (*rod.Page, error) {
	if t.browser == nil || t.pageTargetID == "" {
		return nil, fmt.Errorf("browser page is not initialized")
	}
	page, err := t.browser.Context(ctx).PageFromTarget(t.pageTargetID)
	if err != nil {
		return nil, err
	}
	return page.Context(ctx), nil
}

func (t *BrowserChromeTool) dropBrowserConnection() {
	t.browser = nil
}

func resolveChromeWebSocketURL(ctx context.Context, debugPort string) (string, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%s/json/version", debugPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Chrome debug endpoint returned %s", resp.Status)
	}
	var payload struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode Chrome debug endpoint: %w", err)
	}
	if payload.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("Chrome debug endpoint did not provide webSocketDebuggerUrl")
	}
	return payload.WebSocketDebuggerURL, nil
}

func waitForBrowserChromeRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
