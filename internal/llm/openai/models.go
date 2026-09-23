package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CuratedModels is the newest-first OpenAI chat catalog shown in Caesar and the
// TUI. Live /v1/models is still queried and merged afterwards, but new flagship
// ids such as gpt-6-astra can lag or land on later pages, so they must stay
// selectable even when the first /models page omits them.
var CuratedModels = []string{
	"gpt-6-astra",
	"gpt-6-sol",
	"gpt-6-luna",
	"gpt-5.5",
	"gpt-5.5-pro",
	"gpt-5.4",
	"gpt-5.4-pro",
	"gpt-5.4-mini",
	"gpt-5.4-nano",
	"gpt-5.2",
	"gpt-4.1",
	"gpt-4.1-mini",
	"gpt-4o",
	"gpt-4o-mini",
}

const (
	modelCatalogTimeout = 10 * time.Second
	modelsPageLimit     = 100
	modelsMaxPages      = 20
)

// ModelCatalogOptions configures live discovery for ListModelCatalog.
type ModelCatalogOptions struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

type modelsEndpointResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// ListModelCatalog returns curated OpenAI chat models first, then ids discovered
// from the live /v1/models endpoint (paginated). Discovery failures fall back to
// the curated list so the picker never hides known current models.
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
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: modelCatalogTimeout}
	}
	return discoverModelsFromModelsEndpoint(ctx, client, opts)
}

func discoverModelsFromModelsEndpoint(ctx context.Context, client *http.Client, opts ModelCatalogOptions) []string {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil
	}

	models := make([]string, 0, 32)
	after := ""
	for page := 0; page < modelsMaxPages; page++ {
		endpoint, err := url.Parse(base + "/models")
		if err != nil {
			return models
		}
		query := endpoint.Query()
		query.Set("limit", strconv.Itoa(modelsPageLimit))
		if after != "" {
			query.Set("after", after)
		}
		endpoint.RawQuery = query.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return models
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(opts.APIKey))

		resp, err := client.Do(req)
		if err != nil {
			return models
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return models
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return models
		}

		var parsed modelsEndpointResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return models
		}
		if len(parsed.Data) == 0 {
			return models
		}

		pageLastID := strings.TrimSpace(parsed.LastID)
		for _, model := range parsed.Data {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			models = append(models, id)
			pageLastID = id
		}

		if !parsed.HasMore {
			return models
		}
		nextAfter := strings.TrimSpace(parsed.LastID)
		if nextAfter == "" {
			nextAfter = pageLastID
		}
		if nextAfter == "" || nextAfter == after {
			return models
		}
		after = nextAfter
	}
	return models
}
