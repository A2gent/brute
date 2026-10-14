package integrationtools

// Shared Gmail plumbing: OAuth (loopback + PKCE) helpers, token refresh with persistence,
// access-level enforcement and a tiny Gmail REST client used by gmail_query and gmail_draft.

import (
	"context"
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

	"github.com/A2gent/brute/internal/storage"
)

const (
	// GmailProvider is the integration provider id.
	GmailProvider = "gmail"

	GmailAccessRead  = "read"
	GmailAccessDraft = "draft"

	gmailScopeReadonly = "https://www.googleapis.com/auth/gmail.readonly"
	gmailScopeCompose  = "https://www.googleapis.com/auth/gmail.compose"
	gmailScopeEmail    = "email"

	googleAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
)

// Overridable in tests.
var (
	gmailAPIBase   = "https://gmail.googleapis.com/gmail/v1/users/me"
	googleTokenURL = defaultGoogleTokenURL
)

// GmailAccessLevel returns the configured access level, defaulting to read-only.
func GmailAccessLevel(integration *storage.Integration) string {
	if integration != nil && strings.EqualFold(strings.TrimSpace(integration.Config["access_level"]), GmailAccessDraft) {
		return GmailAccessDraft
	}
	return GmailAccessRead
}

// GmailScopes maps an access level to the OAuth scopes requested from Google.
func GmailScopes(level string) []string {
	scopes := []string{gmailScopeReadonly, gmailScopeEmail}
	if level == GmailAccessDraft {
		scopes = append(scopes, gmailScopeCompose)
	}
	return scopes
}

// NewPKCE returns a random code verifier and its S256 challenge.
func NewPKCE() (verifier string, challenge string, err error) {
	buf := make([]byte, 48)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// GoogleAuthURL builds the Google consent URL. offline + consent guarantee a refresh token.
func GoogleAuthURL(clientID, redirectURI, state, challenge string, scopes []string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return googleAuthURL + "?" + q.Encode()
}

type googleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func postGoogleToken(ctx context.Context, client *http.Client, form url.Values) (*googleTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("google token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out googleTokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return nil, fmt.Errorf("token response did not include access_token")
	}
	return &out, nil
}

// ExchangeGoogleCode swaps an authorization code for tokens and stores them on the integration
// (the caller persists it). It also records the granted scopes. Works for any Google provider.
func ExchangeGoogleCode(ctx context.Context, integration *storage.Integration, code, redirectURI, verifier string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	form := url.Values{}
	form.Set("client_id", strings.TrimSpace(integration.Config["client_id"]))
	form.Set("client_secret", strings.TrimSpace(integration.Config["client_secret"]))
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	form.Set("code_verifier", verifier)
	tok, err := postGoogleToken(ctx, client, form)
	if err != nil {
		return err
	}
	if strings.TrimSpace(tok.RefreshToken) == "" {
		return fmt.Errorf("Google did not return a refresh token; remove the app under myaccount.google.com/permissions and connect again")
	}
	applyGoogleToken(integration, tok)
	integration.Config["refresh_token"] = tok.RefreshToken
	integration.Config["granted_scopes"] = tok.Scope
	return nil
}

// FillGoogleAccountEmail records which Google account was connected, using the provider's own API.
func FillGoogleAccountEmail(ctx context.Context, integration *storage.Integration) error {
	client := &http.Client{Timeout: 30 * time.Second}
	token := strings.TrimSpace(integration.Config["access_token"])
	var (
		email string
		err   error
	)
	switch integration.Provider {
	case GmailProvider:
		email, err = fetchGmailEmail(ctx, client, token)
	default:
		email, err = fetchCalendarOwner(ctx, client, token)
	}
	if err != nil {
		return err
	}
	integration.Config["email"] = email
	return nil
}

func applyGoogleToken(integration *storage.Integration, tok *googleTokenResponse) {
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}
	integration.Config["access_token"] = tok.AccessToken
	if tok.ExpiresIn > 0 {
		integration.Config["token_expiry"] = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
}

func fetchGmailEmail(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	var profile struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := gmailGet(ctx, client, accessToken, "/profile", nil, &profile); err != nil {
		return "", err
	}
	return profile.EmailAddress, nil
}

// GoogleAccessToken returns a valid access token, refreshing and persisting it when needed.
func GoogleAccessToken(ctx context.Context, store storage.Store, client *http.Client, integration *storage.Integration) (string, error) {
	if integration == nil {
		return "", fmt.Errorf("integration is required")
	}
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}
	cfg := integration.Config
	if token, expiry := strings.TrimSpace(cfg["access_token"]), strings.TrimSpace(cfg["token_expiry"]); token != "" && expiry != "" {
		if t, err := time.Parse(time.RFC3339, expiry); err == nil && t.After(time.Now().Add(30*time.Second)) {
			return token, nil
		}
	}
	clientID := strings.TrimSpace(cfg["client_id"])
	clientSecret := strings.TrimSpace(cfg["client_secret"])
	refresh := strings.TrimSpace(cfg["refresh_token"])
	if clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("%s integration %q is missing client_id/client_secret", integration.Provider, integration.Name)
	}
	if refresh == "" {
		return "", fmt.Errorf("%s integration %q is not connected yet; open it in Caesar and press \"Sign in with Google\"", integration.Provider, integration.Name)
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refresh)
	form.Set("grant_type", "refresh_token")
	tok, err := postGoogleToken(ctx, client, form)
	if err != nil {
		if strings.Contains(err.Error(), "invalid_grant") {
			return "", fmt.Errorf("Google rejected the stored refresh token (revoked, or app still in Testing mode for 7+ days); reconnect the integration in Caesar: %w", err)
		}
		return "", err
	}
	applyGoogleToken(integration, tok)
	integration.UpdatedAt = time.Now()
	if store != nil {
		if err := store.SaveIntegration(integration); err != nil {
			return "", fmt.Errorf("refreshed token but failed to persist integration: %w", err)
		}
	}
	return tok.AccessToken, nil
}

// SelectGmailIntegration picks one enabled gmail integration by id/name, or the only one.
func SelectGmailIntegration(store storage.Store, id, name string) (*storage.Integration, error) {
	all, err := store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}
	var candidates []*storage.Integration
	for _, item := range all {
		if item.Provider == GmailProvider && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled gmail integrations found")
	}
	if id = strings.TrimSpace(id); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("gmail integration with id %q not found or disabled", id)
	}
	if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
		var matched []*storage.Integration
		for _, item := range candidates {
			if strings.ToLower(strings.TrimSpace(item.Name)) == name {
				matched = append(matched, item)
			}
		}
		switch len(matched) {
		case 1:
			return matched[0], nil
		case 0:
			return nil, fmt.Errorf("gmail integration named %q not found", name)
		default:
			return nil, fmt.Errorf("multiple gmail integrations matched name %q; pass integration_id", name)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple gmail integrations are enabled; pass integration_id or integration_name")
}

func gmailDo(ctx context.Context, client *http.Client, accessToken, method, path string, query url.Values, body interface{}, out interface{}) error {
	u := gmailAPIBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gmail request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gmail API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("failed to parse gmail response: %w", err)
	}
	return nil
}

func gmailGet(ctx context.Context, client *http.Client, accessToken, path string, query url.Values, out interface{}) error {
	return gmailDo(ctx, client, accessToken, http.MethodGet, path, query, nil, out)
}

// GmailTestSummary is used by the integration "Test" button.
func GmailTestSummary(ctx context.Context, store storage.Store, integration *storage.Integration) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	token, err := GoogleAccessToken(ctx, store, client, integration)
	if err != nil {
		return "", err
	}
	var profile struct {
		EmailAddress  string `json:"emailAddress"`
		MessagesTotal int    `json:"messagesTotal"`
	}
	if err := gmailGet(ctx, client, token, "/profile", nil, &profile); err != nil {
		return "", err
	}
	var inbox struct {
		MessagesUnread int `json:"messagesUnread"`
		MessagesTotal  int `json:"messagesTotal"`
	}
	if err := gmailGet(ctx, client, token, "/labels/INBOX", nil, &inbox); err != nil {
		return "", err
	}
	return fmt.Sprintf("Connected as %s. Inbox: %d messages, %d unread. Access: %s.",
		profile.EmailAddress, inbox.MessagesTotal, inbox.MessagesUnread, GmailAccessLevel(integration)), nil
}
