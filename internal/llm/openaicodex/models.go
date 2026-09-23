package openaicodex

import (
	"context"
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
// and the terminal UI. ListModelCatalog augments it with live discovery from
// the Codex OAuth /models endpoint or the OpenAI-compatible /models endpoint.
var CuratedModels = []string{
	"gpt-6-astra",
	"gpt-6-sol",
	"gpt-6-luna",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.6-codex",
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

var legacyReasoningSuffixes = []string{
	"-low",
	"-medium",
	"-high",
	"-xhigh",
	"-max",
	"-ultra",
}

// NormalizeModelID converts legacy GPT-5.6 usage-bucket names to the exact
// model slug accepted by the Codex responses endpoint. Reasoning effort is a
// separate request field and must not be encoded in the model ID.
func NormalizeModelID(model string) string {
	trimmed := strings.TrimSpace(model)
	lower := strings.ToLower(trimmed)
	for _, base := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if lower == base {
			return base
		}
		for _, suffix := range legacyReasoningSuffixes {
			if lower == base+suffix {
				return base
			}
		}
	}
	return trimmed
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
	// key, ListModelCatalog best-effort queries the Codex /models endpoint and
	// falls back to the curated catalog on failure.
	AccessToken string
	// HTTPClient overrides the default client (used in tests). Optional.
	HTTPClient *http.Client
}

// ListModelCatalog returns the Codex model catalog.
//
// For ChatGPT-account (OAuth) usage ListModelCatalog best-effort queries the
// Codex backend /models endpoint (same contract as codex_cli_rs) and merges
// callable slugs after the curated list. Discovery failures fall back to the
// curated catalog so the picker stays usable offline.
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
		return discoverModelsFromOAuthEndpoint(ctx, client, opts)
	}
	return nil
}

type modelsEndpointResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type codexModelInfo struct {
	Slug           string  `json:"slug"`
	Visibility     *string `json:"visibility"`
	SupportedInAPI *bool   `json:"supported_in_api"`
}

type codexModelsResponse struct {
	Models []codexModelInfo `json:"models"`
}

// NormalizeBaseURL strips trailing slashes and a /responses suffix from Codex base URLs.
func NormalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(base, "/responses")
}

// discoverModelsFromModelsEndpoint queries the OpenAI-compatible /models
// endpoint used in API-key Codex mode and keeps only Codex-relevant ids so the
// catalog is not flooded with embeddings, audio, and image models.
func discoverModelsFromModelsEndpoint(ctx context.Context, client *http.Client, opts ModelCatalogOptions) []string {
	base := NormalizeBaseURL(opts.BaseURL)
	if base == "" {
		return nil
	}
	// OpenAI-compatible /models discovery applies only to non-OAuth hosts.
	if strings.Contains(base, "/backend-api") {
		return nil
	}

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

func discoverModelsFromOAuthEndpoint(ctx context.Context, client *http.Client, opts ModelCatalogOptions) []string {
	base := NormalizeBaseURL(opts.BaseURL)
	if base == "" {
		base = NormalizeBaseURL(defaultBaseURL)
	}

	endpoint, err := url.Parse(base + "/models")
	if err != nil {
		return nil
	}
	query := endpoint.Query()
	query.Set("client_version", ClientVersion)
	endpoint.RawQuery = query.Encode()

	token := strings.TrimSpace(opts.AccessToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-Agent", UserAgent())
	if accountID := extractAccountID(token); accountID != "" {
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
	var parsed codexModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}

	models := make([]string, 0, len(parsed.Models))
	for _, model := range parsed.Models {
		if isOAuthDiscoverableModel(model) {
			models = append(models, strings.TrimSpace(model.Slug))
		}
	}
	return models
}

// isOAuthDiscoverableModel mirrors Codex picker rules for ChatGPT-account auth:
// visibility "list" models are picker-visible; hidden/none are excluded when set.
// supported_in_api is not filtered in OAuth mode. Missing optional fields are kept.
func isOAuthDiscoverableModel(model codexModelInfo) bool {
	if !looksLikeModelID(model.Slug) {
		return false
	}
	if model.Visibility != nil {
		switch strings.ToLower(strings.TrimSpace(*model.Visibility)) {
		case "hide", "none":
			return false
		}
	}
	return true
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
