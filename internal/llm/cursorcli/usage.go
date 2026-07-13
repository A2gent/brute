package cursorcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	cursorAccessTokenEnv          = "AAGENT_CURSOR_ACCESS_TOKEN"
	cursorUsageURLEnv             = "AAGENT_CURSOR_USAGE_URL"
	cursorSkipPlatformAuthEnv     = "AAGENT_CURSOR_SKIP_PLATFORM_AUTH"
	defaultCursorUsageAPIURL      = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	cursorUsageTimeout       = 15 * time.Second

	macOSKeychainService = "cursor-access-token"
	macOSKeychainAccount = "cursor-user"
)

// PlanUsage captures Cursor included-usage buckets returned by GetCurrentPeriodUsage.
type PlanUsage struct {
	AutoPercentUsed  float64 `json:"autoPercentUsed"`
	APIPercentUsed   float64 `json:"apiPercentUsed"`
	TotalPercentUsed float64 `json:"totalPercentUsed"`
	Limit            int64   `json:"limit"`
	TotalSpend       int64   `json:"totalSpend"`
	IncludedSpend    int64   `json:"includedSpend"`
}

// PeriodUsage is the subset of Cursor dashboard usage data Brute needs for provider usage UI.
type PeriodUsage struct {
	BillingCycleStart int64     `json:"billingCycleStart"`
	BillingCycleEnd   int64     `json:"billingCycleEnd"`
	PlanUsage         PlanUsage `json:"planUsage"`
	DisplayMessage    string    `json:"displayMessage"`
}

type cursorAuthFile struct {
	AccessToken string `json:"accessToken"`
}

// ResolveAccessToken returns the Cursor OAuth access token used by Cursor Agent CLI.
func ResolveAccessToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv(cursorAccessTokenEnv)); token != "" {
		return token, nil
	}
	if token, err := resolveAccessTokenFromPlatform(); err == nil && token != "" {
		return token, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	for _, path := range cursorAuthFileCandidates() {
		token, err := resolveAccessTokenFromAuthFile(path)
		if err == nil && token != "" {
			return token, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("Cursor Agent CLI access token not found; run `agent login` or set %s", cursorAccessTokenEnv)
}

func resolveAccessTokenFromPlatform() (string, error) {
	if envBoolDefault(cursorSkipPlatformAuthEnv, false) {
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

func cursorAuthFileCandidates() []string {
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

// FetchPeriodUsage loads current billing-period usage from Cursor dashboard RPC.
func FetchPeriodUsage(ctx context.Context, client *http.Client, usageURL, accessToken string) (PeriodUsage, error) {
	if client == nil {
		client = http.DefaultClient
	}
	usageURL = strings.TrimSpace(usageURL)
	if usageURL == "" {
		usageURL = strings.TrimSpace(os.Getenv(cursorUsageURLEnv))
	}
	if usageURL == "" {
		usageURL = defaultCursorUsageAPIURL
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return PeriodUsage{}, fmt.Errorf("missing Cursor access token")
	}

	requestCtx, cancel := context.WithTimeout(ctx, cursorUsageTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, usageURL, strings.NewReader("{}"))
	if err != nil {
		return PeriodUsage{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return PeriodUsage{}, fmt.Errorf("failed to fetch Cursor usage: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return PeriodUsage{}, fmt.Errorf("failed to read Cursor usage response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PeriodUsage{}, fmt.Errorf("Cursor usage unavailable (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload PeriodUsage
	if err := json.Unmarshal(body, &payload); err != nil {
		return PeriodUsage{}, fmt.Errorf("failed to parse Cursor usage response: %w", err)
	}
	return payload, nil
}

// FormatPeriodUsageSummary renders a compact usage-left summary for provider cards.
func FormatPeriodUsageSummary(usage PeriodUsage) string {
	parts := make([]string, 0, 4)
	if msg := strings.TrimSpace(usage.DisplayMessage); msg != "" {
		parts = append(parts, msg)
	}
	if usage.PlanUsage.TotalPercentUsed > 0 || usage.PlanUsage.AutoPercentUsed > 0 || usage.PlanUsage.APIPercentUsed > 0 {
		parts = append(parts,
			"total "+formatCursorUsageWindow(usage.PlanUsage.TotalPercentUsed),
			"auto "+formatCursorUsageWindow(usage.PlanUsage.AutoPercentUsed),
			"api "+formatCursorUsageWindow(usage.PlanUsage.APIPercentUsed),
		)
	}
	if len(parts) == 0 {
		return "Cursor usage endpoint responded but did not include plan usage details."
	}
	return strings.Join(parts, " · ")
}

func formatCursorUsageWindow(usedPercent float64) string {
	used := clampCursorPercent(usedPercent)
	left := int(math.Max(0, math.Round(100-used)))
	return fmt.Sprintf("%d%% left", left)
}

func clampCursorPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
