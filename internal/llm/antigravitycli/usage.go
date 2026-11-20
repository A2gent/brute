package antigravitycli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	defaultQuotaSummaryURL = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	quotaSummaryTimeout    = 15 * time.Second
	quotaSummaryUserAgent  = "antigravity"
	accessTokenEnv         = "AAGENT_ANTIGRAVITY_ACCESS_TOKEN"
	macOSKeychainService   = "gemini"
	macOSKeychainAccount   = "antigravity"
	keyringBase64Prefix    = "go-keyring-base64:"
)

type QuotaSummary struct {
	Groups []QuotaGroup `json:"groups"`
}

type QuotaGroup struct {
	DisplayName string        `json:"displayName"`
	Buckets     []QuotaBucket `json:"buckets"`
}

type QuotaBucket struct {
	BucketID          string  `json:"bucketId"`
	DisplayName       string  `json:"displayName"`
	Window            string  `json:"window"`
	ResetTime         string  `json:"resetTime"`
	RemainingFraction float64 `json:"remainingFraction"`
}

type keyringToken struct {
	Token struct {
		AccessToken string `json:"access_token"`
	} `json:"token"`
}

// ResolveAccessToken returns the Antigravity OAuth access token stored by agy login.
func ResolveAccessToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv(accessTokenEnv)); token != "" {
		return token, nil
	}
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("Antigravity access token is only read from the macOS keychain; set %s", accessTokenEnv)
	}
	out, err := exec.Command("security", "find-generic-password", "-s", macOSKeychainService, "-a", macOSKeychainAccount, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("Antigravity login not found in keychain; run `agy` and sign in")
	}
	raw := strings.TrimSpace(string(out))
	if !strings.HasPrefix(raw, keyringBase64Prefix) {
		return "", fmt.Errorf("unexpected Antigravity keychain entry format")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, keyringBase64Prefix))
	if err != nil {
		return "", fmt.Errorf("failed to decode Antigravity keychain entry: %w", err)
	}
	var payload keyringToken
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return "", fmt.Errorf("failed to parse Antigravity keychain entry: %w", err)
	}
	token := strings.TrimSpace(payload.Token.AccessToken)
	if token == "" {
		return "", fmt.Errorf("Antigravity keychain entry does not contain an access token")
	}
	return token, nil
}

// FetchQuotaSummary loads weekly and 5-hour quota buckets from the Antigravity backend.
// WHY: the endpoint rejects requests without the agy User-Agent with 403.
func FetchQuotaSummary(ctx context.Context, client *http.Client, url, accessToken string) (QuotaSummary, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(url) == "" {
		url = defaultQuotaSummaryURL
	}
	requestCtx, cancel := context.WithTimeout(ctx, quotaSummaryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return QuotaSummary{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", quotaSummaryUserAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return QuotaSummary{}, fmt.Errorf("failed to fetch Antigravity quota: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QuotaSummary{}, fmt.Errorf("failed to read Antigravity quota response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QuotaSummary{}, fmt.Errorf("Antigravity quota unavailable (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var summary QuotaSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return QuotaSummary{}, fmt.Errorf("failed to parse Antigravity quota response: %w", err)
	}
	return summary, nil
}
