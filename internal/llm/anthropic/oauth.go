package anthropic

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Claude Code public client ID
	clientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	authURL     = "https://claude.ai/oauth/authorize"
	tokenURL    = "https://console.anthropic.com/v1/oauth/token"
	redirectURI = "https://console.anthropic.com/oauth/code/callback"
)

// OAuthTokens represents OAuth access and refresh tokens
type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	// ExpiresAt is an absolute Unix timestamp (seconds). When non-zero it takes
	// precedence over ExpiresIn for expiry checks so the value stays accurate
	// across the lifetime of a long-lived client.
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

// PKCEChallenge contains PKCE verifier and challenge
type PKCEChallenge struct {
	Verifier  string
	Challenge string
}

// GeneratePKCEChallenge creates a PKCE code verifier and challenge
func GeneratePKCEChallenge() (*PKCEChallenge, error) {
	// Generate random verifier (43-128 characters)
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, fmt.Errorf("failed to generate verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Create SHA256 challenge
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	return &PKCEChallenge{
		Verifier:  verifier,
		Challenge: challenge,
	}, nil
}

// BuildAuthorizationURL creates the OAuth authorization URL
func BuildAuthorizationURL(pkce *PKCEChallenge) string {
	params := url.Values{}
	params.Set("code", "true")
	params.Set("client_id", clientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", "org:create_api_key user:profile user:inference")
	params.Set("code_challenge", pkce.Challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", pkce.Verifier)

	return authURL + "?" + params.Encode()
}

// ExchangeCodeForTokens exchanges authorization code for access tokens.
// The code is in the format "authcode#verifier" as returned by the
// claude.ai OAuth callback page — the state/verifier is embedded in the
// pasted string, so we always derive code_verifier from the code itself.
// The separate verifier parameter is kept for backwards compatibility but
// is only used as a fallback when the code contains no "#" separator.
func ExchangeCodeForTokens(code, verifier string) (*OAuthTokens, error) {
	// Parse code (format: "authcode#verifier")
	parts := strings.Split(code, "#")
	authCode := parts[0]
	state := ""
	if len(parts) > 1 {
		// The state echoed back by the auth server IS the PKCE verifier
		// (we set state=verifier in BuildAuthorizationURL). Always use it
		// as code_verifier so the exchange works even when the frontend
		// loses the separately stored verifier (e.g. after a page refresh).
		state = parts[1]
		verifier = parts[1]
	}

	payload := map[string]string{
		"code":          authCode,
		"state":         state,
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	}

	return requestTokens(payload)
}

// RefreshAccessToken refreshes an expired access token
func RefreshAccessToken(refreshToken string) (*OAuthTokens, error) {
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
	}

	return requestTokens(payload)
}

// requestTokens makes a token request to Anthropic OAuth endpoint
func requestTokens(payload map[string]string) (*OAuthTokens, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokens OAuthTokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse tokens: %w", err)
	}

	return &tokens, nil
}

// IsTokenExpired checks if an access token is expired
func IsTokenExpired(expiresAt int64) bool {
	// Add 5 minute buffer to refresh before actual expiration
	return time.Now().Unix() >= (expiresAt - 300)
}
