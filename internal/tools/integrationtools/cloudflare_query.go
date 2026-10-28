package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const (
	cloudflareDefaultBaseURL    = "https://api.cloudflare.com/client/v4"
	cloudflareDefaultMaxResults = 25
	cloudflareMaxResults        = 100
	cloudflareResponseLimit     = 8 * 1024 * 1024
	cloudflareDefaultDays       = 7
	cloudflareMaxAnalyticsDays  = 30
)

// CloudflareQueryTool exposes read-only Cloudflare API access through configured Cloudflare
// integrations so agents can inspect zones, DNS, Workers, Pages and traffic without mutating state.
type CloudflareQueryTool struct {
	store  storage.Store
	client *http.Client
}

type CloudflareQueryParams struct {
	Operation       string `json:"operation"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	Zone            string `json:"zone,omitempty"`
	AccountID       string `json:"account_id,omitempty"`
	Project         string `json:"project,omitempty"`
	RecordType      string `json:"record_type,omitempty"`
	Name            string `json:"name,omitempty"`
	Days            int    `json:"days,omitempty"`
	Page            int    `json:"page,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
}

type cloudflareCredentials struct {
	Token     string
	AccountID string
	BaseURL   string
	Source    string
}

func NewCloudflareQueryTool(store storage.Store) *CloudflareQueryTool {
	return &CloudflareQueryTool{
		store:  store,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *CloudflareQueryTool) Name() string {
	return "cloudflare_query"
}

func (t *CloudflareQueryTool) Description() string {
	return "Query Cloudflare using a configured integration. Read-only operations inspect accounts, zones (domains), DNS records, traffic analytics, Workers and Pages projects."
}

func (t *CloudflareQueryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "Read-only operation: list_accounts, list_zones, get_zone, list_dns_records, zone_analytics, list_workers, list_pages_projects, or list_pages_deployments",
				"enum":        []string{"list_accounts", "list_zones", "get_zone", "list_dns_records", "zone_analytics", "list_workers", "list_pages_projects", "list_pages_deployments"},
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific Cloudflare integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific Cloudflare integration name to use (optional)",
			},
			"zone": map[string]interface{}{
				"type":        "string",
				"description": "Zone ID or domain name (e.g. example.com). Required for get_zone, list_dns_records and zone_analytics; list_zones uses it as a name filter",
			},
			"account_id": map[string]interface{}{
				"type":        "string",
				"description": "Account ID for Workers/Pages operations (optional; defaults to the integration's account, or the only account the token can see)",
			},
			"project": map[string]interface{}{
				"type":        "string",
				"description": "Pages project name, required for list_pages_deployments",
			},
			"record_type": map[string]interface{}{
				"type":        "string",
				"description": "DNS record type filter for list_dns_records, e.g. A, CNAME, MX, TXT",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "DNS record name filter for list_dns_records (full hostname)",
			},
			"days": map[string]interface{}{
				"type":        "integer",
				"description": "Look-back window in days for zone_analytics (1-30, default 7)",
			},
			"page": map[string]interface{}{
				"type":        "integer",
				"description": "Page number for list operations (default 1)",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Rows per page (1-100, default 25)",
			},
		},
		"required": []string{"operation"},
	}
}

func (t *CloudflareQueryTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p CloudflareQueryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	operation := strings.ToLower(strings.TrimSpace(p.Operation))
	if operation == "" {
		return &tools.Result{Success: false, Error: "operation is required"}, nil
	}

	creds, err := t.resolveCredentials(ctx, p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	output, err := t.executeRead(ctx, creds, operation, p)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	return &tools.Result{Success: true, Output: output}, nil
}

func (t *CloudflareQueryTool) executeRead(ctx context.Context, creds cloudflareCredentials, operation string, p CloudflareQueryParams) (string, error) {
	switch operation {
	case "list_accounts":
		return t.doGet(ctx, creds, "/accounts", cloudflarePaginationQuery(p))
	case "list_zones":
		query := cloudflarePaginationQuery(p)
		if zone := strings.TrimSpace(p.Zone); zone != "" {
			query.Set("name", zone)
		}
		return t.doGet(ctx, creds, "/zones", query)
	case "get_zone":
		zoneID, err := t.resolveZoneID(ctx, creds, p.Zone)
		if err != nil {
			return "", err
		}
		return t.doGet(ctx, creds, "/zones/"+url.PathEscape(zoneID), nil)
	case "list_dns_records":
		zoneID, err := t.resolveZoneID(ctx, creds, p.Zone)
		if err != nil {
			return "", err
		}
		query := cloudflarePaginationQuery(p)
		if v := strings.TrimSpace(p.RecordType); v != "" {
			query.Set("type", strings.ToUpper(v))
		}
		if v := strings.TrimSpace(p.Name); v != "" {
			query.Set("name", v)
		}
		return t.doGet(ctx, creds, "/zones/"+url.PathEscape(zoneID)+"/dns_records", query)
	case "zone_analytics":
		zoneID, err := t.resolveZoneID(ctx, creds, p.Zone)
		if err != nil {
			return "", err
		}
		return t.zoneAnalytics(ctx, creds, zoneID, p.Days)
	case "list_workers":
		accountID, err := t.resolveAccountID(ctx, creds, p.AccountID)
		if err != nil {
			return "", err
		}
		return t.doGet(ctx, creds, "/accounts/"+url.PathEscape(accountID)+"/workers/scripts", nil)
	case "list_pages_projects":
		accountID, err := t.resolveAccountID(ctx, creds, p.AccountID)
		if err != nil {
			return "", err
		}
		return t.doGet(ctx, creds, "/accounts/"+url.PathEscape(accountID)+"/pages/projects", cloudflarePaginationQuery(p))
	case "list_pages_deployments":
		project := strings.TrimSpace(p.Project)
		if project == "" {
			return "", fmt.Errorf("project is required")
		}
		accountID, err := t.resolveAccountID(ctx, creds, p.AccountID)
		if err != nil {
			return "", err
		}
		return t.doGet(ctx, creds, "/accounts/"+url.PathEscape(accountID)+"/pages/projects/"+url.PathEscape(project)+"/deployments", cloudflarePaginationQuery(p))
	default:
		return "", fmt.Errorf("unsupported operation %q", operation)
	}
}

func (t *CloudflareQueryTool) doGet(ctx context.Context, creds cloudflareCredentials, path string, query url.Values) (string, error) {
	body, err := cloudflareRequest(ctx, t.client, creds, http.MethodGet, path, query, nil)
	if err != nil {
		return "", err
	}
	return formatCloudflareBody(creds, path, body)
}

func formatCloudflareBody(creds cloudflareCredentials, label string, body []byte) (string, error) {
	var payload interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode Cloudflare response: %w", err)
	}
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format Cloudflare response: %w", err)
	}
	return fmt.Sprintf("Cloudflare %s response from %s\n%s", label, creds.Source, pretty), nil
}

// zoneAnalytics reads daily HTTP traffic totals for a zone from the GraphQL Analytics API,
// which is available on every plan (including Free) through httpRequests1dGroups.
func (t *CloudflareQueryTool) zoneAnalytics(ctx context.Context, creds cloudflareCredentials, zoneID string, days int) (string, error) {
	if days <= 0 {
		days = cloudflareDefaultDays
	} else if days > cloudflareMaxAnalyticsDays {
		days = cloudflareMaxAnalyticsDays
	}
	now := time.Now().UTC()
	vars := map[string]interface{}{
		"zone":  zoneID,
		"since": now.AddDate(0, 0, -(days - 1)).Format("2006-01-02"),
		"until": now.Format("2006-01-02"),
		"limit": days,
	}
	const query = `query ($zone: String!, $since: Date!, $until: Date!, $limit: Int!) {
  viewer {
    zones(filter: {zoneTag: $zone}) {
      httpRequests1dGroups(limit: $limit, filter: {date_geq: $since, date_leq: $until}, orderBy: [date_ASC]) {
        dimensions { date }
        sum {
          requests
          cachedRequests
          bytes
          cachedBytes
          threats
          pageViews
          responseStatusMap { edgeResponseStatus requests }
          countryMap { clientCountryName requests }
        }
        uniq { uniques }
      }
    }
  }
}`
	payload, err := json.Marshal(map[string]interface{}{"query": query, "variables": vars})
	if err != nil {
		return "", err
	}
	body, err := cloudflareRequest(ctx, t.client, creds, http.MethodPost, "/graphql", nil, payload)
	if err != nil {
		return "", err
	}
	// GraphQL reports query problems with HTTP 200 and an "errors" array.
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &envelope) == nil && len(envelope.Errors) > 0 {
		return "", fmt.Errorf("Cloudflare analytics error: %s%s", envelope.Errors[0].Message, cloudflareErrorHint(http.StatusForbidden))
	}
	return formatCloudflareBody(creds, fmt.Sprintf("zone analytics (last %d days)", days), body)
}

// cloudflareLooksLikeID reports whether s is a 32-char hex Cloudflare identifier.
func cloudflareLooksLikeID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// resolveZoneID accepts a zone ID or a domain name and returns the zone ID.
func (t *CloudflareQueryTool) resolveZoneID(ctx context.Context, creds cloudflareCredentials, zone string) (string, error) {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return "", fmt.Errorf("zone is required (zone ID or domain name; use list_zones to find it)")
	}
	if cloudflareLooksLikeID(strings.ToLower(zone)) {
		return strings.ToLower(zone), nil
	}
	query := url.Values{}
	query.Set("name", strings.ToLower(zone))
	body, err := cloudflareRequest(ctx, t.client, creds, http.MethodGet, "/zones", query, nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("failed to decode Cloudflare zones: %w", err)
	}
	if len(resp.Result) == 0 {
		return "", fmt.Errorf("no zone named %q is visible to this token", zone)
	}
	return resp.Result[0].ID, nil
}

// resolveAccountID picks an explicit account, the integration's account, or the only account the token can see.
func (t *CloudflareQueryTool) resolveAccountID(ctx context.Context, creds cloudflareCredentials, explicit string) (string, error) {
	if id := strings.TrimSpace(explicit); id != "" {
		return id, nil
	}
	return cloudflareAccountID(ctx, t.client, creds)
}

func cloudflareAccountID(ctx context.Context, client *http.Client, creds cloudflareCredentials) (string, error) {
	if creds.AccountID != "" {
		return creds.AccountID, nil
	}
	accounts, err := cloudflareListAccounts(ctx, client, creds)
	if err != nil {
		return "", err
	}
	switch len(accounts) {
	case 0:
		return "", fmt.Errorf("this token cannot see any account; add the Account Settings read permission or set Account ID in the integration")
	case 1:
		return accounts[0].ID, nil
	}
	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		names = append(names, fmt.Sprintf("%s (%s)", a.Name, a.ID))
	}
	return "", fmt.Errorf("token can see several accounts (%s); pass account_id or set Account ID in the integration", strings.Join(names, ", "))
}

type cloudflareAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func cloudflareListAccounts(ctx context.Context, client *http.Client, creds cloudflareCredentials) ([]cloudflareAccount, error) {
	query := url.Values{}
	query.Set("per_page", "50")
	body, err := cloudflareRequest(ctx, client, creds, http.MethodGet, "/accounts", query, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result []cloudflareAccount `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode Cloudflare accounts: %w", err)
	}
	return resp.Result, nil
}

// cloudflareRequest performs an authenticated call and returns the raw body. REST responses
// carry {"success":false,"errors":[...]}; those are turned into errors even on HTTP 200.
func cloudflareRequest(ctx context.Context, client *http.Client, creds cloudflareCredentials, method, path string, query url.Values, payload []byte) ([]byte, error) {
	reqURL := strings.TrimRight(creds.BaseURL, "/") + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloudflare request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, cloudflareResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("failed to read Cloudflare response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Cloudflare API error (status %d): %s%s", resp.StatusCode, cloudflareErrorMessage(body, resp.Status), cloudflareErrorHint(resp.StatusCode))
	}
	var envelope struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Success != nil && !*envelope.Success {
		return nil, fmt.Errorf("Cloudflare API error: %s", cloudflareErrorMessage(body, "request failed"))
	}
	return body, nil
}

// cloudflareErrorMessage extracts "code: message" pairs from a Cloudflare error envelope.
func cloudflareErrorMessage(body []byte, fallback string) string {
	var envelope struct {
		Errors []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &envelope) == nil && len(envelope.Errors) > 0 {
		parts := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			parts = append(parts, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
		}
		return strings.Join(parts, "; ")
	}
	if msg := strings.TrimSpace(string(body)); msg != "" {
		return msg
	}
	return fallback
}

// cloudflareErrorHint explains the usual causes of auth failures so the Integrations "Test" result is actionable.
func cloudflareErrorHint(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return " (hint: the API token is invalid, expired or revoked; create a new one. Use an API Token, not the Global API Key)"
	case http.StatusForbidden:
		return " (hint: the token lacks a permission for this call; edit the token and add the read permissions listed in the setup steps, and make sure its Zone/Account Resources include what you query)"
	}
	return ""
}

func cloudflarePaginationQuery(p CloudflareQueryParams) url.Values {
	query := url.Values{}
	limit := p.MaxResults
	if limit <= 0 {
		limit = cloudflareDefaultMaxResults
	} else if limit > cloudflareMaxResults {
		limit = cloudflareMaxResults
	}
	query.Set("per_page", strconv.Itoa(limit))
	if p.Page > 1 {
		query.Set("page", strconv.Itoa(p.Page))
	}
	return query
}

func (t *CloudflareQueryTool) resolveCredentials(ctx context.Context, integrationID string, integrationName string) (cloudflareCredentials, error) {
	if t.store == nil {
		return cloudflareCredentials{}, fmt.Errorf("cloudflare integration is required; configure one in Integrations")
	}
	integration, err := t.selectIntegration(ctx, integrationID, integrationName)
	if err != nil {
		return cloudflareCredentials{}, err
	}
	return cloudflareCredentialsFromIntegration(integration)
}

func (t *CloudflareQueryTool) selectIntegration(ctx context.Context, integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := listIntegrations(ctx, t.store)
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item != nil && item.Provider == "cloudflare" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled cloudflare integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("cloudflare integration with id %q not found or disabled", id)
	}

	if name := strings.ToLower(strings.TrimSpace(integrationName)); name != "" {
		var matched []*storage.Integration
		for _, item := range candidates {
			if strings.ToLower(strings.TrimSpace(item.Name)) == name {
				matched = append(matched, item)
			}
		}
		if len(matched) == 1 {
			return matched[0], nil
		}
		if len(matched) > 1 {
			return nil, fmt.Errorf("multiple cloudflare integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("cloudflare integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple cloudflare integrations are enabled; pass integration_id or integration_name")
}

// cloudflareCredentialsFromIntegration validates and normalizes Cloudflare integration config.
func cloudflareCredentialsFromIntegration(integration *storage.Integration) (cloudflareCredentials, error) {
	if integration == nil {
		return cloudflareCredentials{}, fmt.Errorf("integration is required")
	}
	config := integration.Config
	creds := cloudflareCredentials{
		Token:     strings.TrimSpace(config["api_token"]),
		AccountID: strings.TrimSpace(config["account_id"]),
		BaseURL:   strings.TrimRight(strings.TrimSpace(config["base_url"]), "/"),
		Source:    strings.TrimSpace(integration.Name),
	}
	if creds.BaseURL == "" {
		creds.BaseURL = cloudflareDefaultBaseURL
	}
	if creds.Source == "" {
		creds.Source = integration.ID
	}
	if creds.Token == "" {
		return cloudflareCredentials{}, fmt.Errorf("selected cloudflare integration is missing api_token")
	}
	parsed, err := url.Parse(creds.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return cloudflareCredentials{}, fmt.Errorf("cloudflare base_url must be an absolute URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return cloudflareCredentials{}, fmt.Errorf("cloudflare base_url must use http or https")
	}
	return creds, nil
}

// ProbeCloudflare checks the token of an integration (saved or not) and summarizes what it can
// see, so the Integrations "Test" result tells the user which account and how many domains were found.
// It does not touch the integration store.
func ProbeCloudflare(ctx context.Context, integration *storage.Integration) (string, error) {
	creds, err := cloudflareCredentialsFromIntegration(integration)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 20 * time.Second}

	// User-owned tokens verify at /user/tokens/verify; account-owned tokens only at
	// /accounts/{id}/tokens/verify, so fall back to it when an account ID is configured.
	verifyPath := "/user/tokens/verify"
	if _, err := cloudflareRequest(ctx, client, creds, http.MethodGet, verifyPath, nil, nil); err != nil {
		if creds.AccountID == "" {
			return "", err
		}
		verifyPath = "/accounts/" + url.PathEscape(creds.AccountID) + "/tokens/verify"
		if _, err2 := cloudflareRequest(ctx, client, creds, http.MethodGet, verifyPath, nil, nil); err2 != nil {
			return "", err2
		}
	}

	var parts []string
	if accounts, err := cloudflareListAccounts(ctx, client, creds); err == nil && len(accounts) > 0 {
		names := make([]string, 0, len(accounts))
		for _, a := range accounts {
			names = append(names, fmt.Sprintf("%s (ID %s)", a.Name, a.ID))
		}
		parts = append(parts, "account: "+strings.Join(names, ", "))
	} else {
		parts = append(parts, "no account visible (add the Account Settings read permission to enable Workers/Pages and account ID discovery)")
	}

	query := url.Values{}
	query.Set("per_page", "5")
	if body, err := cloudflareRequest(ctx, client, creds, http.MethodGet, "/zones", query, nil); err == nil {
		var resp struct {
			Result     []struct{ Name string } `json:"result"`
			ResultInfo struct {
				TotalCount int `json:"total_count"`
			} `json:"result_info"`
		}
		if json.Unmarshal(body, &resp) == nil {
			names := make([]string, 0, len(resp.Result))
			for _, z := range resp.Result {
				names = append(names, z.Name)
			}
			msg := fmt.Sprintf("%d domain(s)", resp.ResultInfo.TotalCount)
			if len(names) > 0 {
				msg += ": " + strings.Join(names, ", ")
				if resp.ResultInfo.TotalCount > len(names) {
					msg += ", ..."
				}
			}
			parts = append(parts, msg)
		}
	} else {
		parts = append(parts, "domains not readable (add the Zone Read permission)")
	}
	return "Cloudflare token is valid; " + strings.Join(parts, "; ") + ".", nil
}
