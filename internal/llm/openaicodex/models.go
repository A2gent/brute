package openaicodex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// CuratedModels is the curated catalog of Codex models known to be callable,
// ordered newest first. It is the single source of truth shared by the HTTP API
// and the terminal UI, and the complete list for ChatGPT-account (OAuth) usage.
// In API-key mode ListModelCatalog augments it with models discovered live from
// the OpenAI /models endpoint.
var CuratedModels = []string{
	"gpt-5.6-codex",
	"gpt-5.6-sol-medium",
	"gpt-5.6-terra-medium",
	"gpt-5.5",
	"gpt-5.5-pro",
	"gpt-5.4",
	"gpt-5.4-pro",
	"gpt-5.4-mini",
	"gpt-5.4-nano",
	"gpt-5.3-codex",
	"gpt-5.2",
	"gpt-5.2-codex",
	"gpt-5.1-codex",
	"gpt-5.1-codex-max",
	"gpt-5.1-codex-mini",
}

const modelCatalogTimeout = 10 * time.Second

// ModelCatalogOptions configures live discovery for ListModelCatalog. All
// fields are optional; with none set, ListModelCatalog returns CuratedModels.
type ModelCatalogOptions struct {
	// BaseURL is the configured Codex base URL. Empty falls back to the Codex
	// OAuth backend.
	BaseURL string
	// APIKey is an OpenAI API key. When set (API-key Codex mode), ListModelCatalog
	// discovers models from the OpenAI-compatible /models endpoint. These are the
	// only models the API-key backend will actually accept.
	APIKey string
	// AccessToken is a ChatGPT-account Codex OAuth token. When set without an API
	// key, ListModelCatalog discovers callable models from the usage endpoint's
	// per-model rate-limit buckets (excluding non-callable spark buckets).
	AccessToken string
	// HTTPClient overrides the default client (used in tests). Optional.
	HTTPClient *http.Client
}

// ListModelCatalog returns the Codex model catalog.
//
// For ChatGPT-account (OAuth) usage the OAuth backend exposes no /models
// endpoint. When AccessToken is set, ListModelCatalog augments the curated list
// with model ids from the usage endpoint's per-model buckets, omitting buckets
// the account cannot call (e.g. gpt-5.3-codex-spark).
//
// In API-key mode the OpenAI-compatible /models endpoint is authoritative — its
// ids are genuinely callable — so those are merged in after the curated list.
// It never returns an empty slice, keeps curated models in their intended
// display order, and appends discovered ids afterwards (sorted). Discovery
// failures are ignored so the curated list is always available offline.
func ListModelCatalog(ctx context.Context, opts ModelCatalogOptions) []string {
	seen := make(map[string]bool, len(CuratedModels)+8)
	ordered := make([]string, 0, len(CuratedModels)+8)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ordered = append(ordered, id)
	}

	for _, id := range CuratedModels {
		add(id)
	}

	discovered := discoverModels(ctx, opts)
	sort.Strings(discovered)
	for _, id := range discovered {
		add(id)
	}
	return ordered
}

func discoverModels(ctx context.Context, opts ModelCatalogOptions) []string {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: modelCatalogTimeout}
	}
	if strings.TrimSpace(opts.APIKey) != "" {
		return discoverModelsFromModelsEndpoint(ctx, client, opts)
	}
	if strings.TrimSpace(opts.AccessToken) != "" {
		return discoverModelsFromUsageEndpoint(ctx, client, opts)
	}
	return nil
}

type modelsEndpointResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// discoverModelsFromModelsEndpoint queries the OpenAI-compatible /models
// endpoint used in API-key Codex mode and keeps only Codex-relevant ids so the
// catalog is not flooded with embeddings, audio, and image models.
func discoverModelsFromModelsEndpoint(ctx context.Context, client *http.Client, opts ModelCatalogOptions) []string {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil
	}
	// The OAuth backend (chatgpt.com/backend-api/codex) has no /models endpoint;
	// only attempt discovery against OpenAI-compatible hosts.
	if strings.Contains(base, "/backend-api") {
		return nil
	}
	base = strings.TrimSuffix(base, "/responses")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(opts.APIKey))

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var parsed modelsEndpointResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}

	models := make([]string, 0, len(parsed.Data))
	for _, model := range parsed.Data {
		if looksLikeModelID(model.ID) {
			models = append(models, strings.TrimSpace(model.ID))
		}
	}
	return models
}

type usageDiscoveryResponse struct {
	AdditionalRateLimits []struct {
		LimitName      string `json:"limit_name"`
		MeteredFeature string `json:"metered_feature"`
	} `json:"additional_rate_limits"`
}

// discoverModelsFromUsageEndpoint reads callable model ids from Codex OAuth usage
// buckets. Spark and other non-callable buckets are filtered out.
func discoverModelsFromUsageEndpoint(ctx context.Context, client *http.Client, opts ModelCatalogOptions) []string {
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	usageURL, err := UsageURL(baseURL)
	if err != nil {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return nil
	}
	accessToken := strings.TrimSpace(opts.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-agent", "codex_cli_rs/0.143.0")
	if accountID := codexAccountIDFromToken(accessToken); accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var parsed usageDiscoveryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}

	models := make([]string, 0, len(parsed.AdditionalRateLimits))
	for _, item := range parsed.AdditionalRateLimits {
		if IsNonCallableOAuthUsageLimit(item.LimitName, item.MeteredFeature) {
			continue
		}
		if name := strings.TrimSpace(item.LimitName); looksLikeModelID(name) {
			models = append(models, name)
		}
	}
	return models
}

func codexAccountIDFromToken(accessToken string) string {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if id, ok := claims["chatgpt_account_id"].(string); ok && strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	if raw, ok := claims["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if id, ok := raw["chatgpt_account_id"].(string); ok {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// IsNonCallableOAuthUsageLimit reports whether a Codex OAuth usage bucket refers
// to a model the account cannot invoke. The usage endpoint surfaces per-model
// quota buckets (e.g. gpt-5.3-codex-spark) that ListModelCatalog already omits.
func IsNonCallableOAuthUsageLimit(limitName, meteredFeature string) bool {
	combined := strings.ToLower(strings.TrimSpace(limitName) + " " + strings.TrimSpace(meteredFeature))
	return strings.Contains(combined, "spark")
}

// looksLikeModelID keeps discovery focused on Codex-family chat models and
// filters out non-model rate-limit features and unrelated OpenAI endpoints.
func looksLikeModelID(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "codex" {
		return false
	}
	if strings.HasPrefix(v, "gpt-") {
		return true
	}
	return strings.Contains(v, "codex") || strings.Contains(v, "-sol") || strings.Contains(v, "-terra")
}

// UsageURL derives the ChatGPT usage/rate-limit endpoint from a Codex base URL.
// Codex responses use /backend-api/codex, while usage lives one level up under
// /backend-api/wham/usage. It is exported so the HTTP usage handler and model
// discovery share one URL derivation.
func UsageURL(codexBaseURL string) (string, error) {
	raw := strings.TrimSpace(codexBaseURL)
	if raw == "" {
		raw = defaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Codex base URL %q", codexBaseURL)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(parsed.Path, "/codex") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/codex")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/wham/usage"
	return parsed.String(), nil
}
