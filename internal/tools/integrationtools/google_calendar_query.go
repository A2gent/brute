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
	defaultGoogleTokenURL      = "https://oauth2.googleapis.com/token"
	googleCalendarListEndpoint = "https://www.googleapis.com/calendar/v3/users/me/calendarList"
	googleCalendarEventsBase   = "https://www.googleapis.com/calendar/v3/calendars"
)

// GoogleCalendarQueryTool queries Google Calendar using configured integrations.
type GoogleCalendarQueryTool struct {
	store  storage.Store
	client *http.Client
}

type GoogleCalendarQueryParams struct {
	Operation       string `json:"operation"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	CalendarID      string `json:"calendar_id,omitempty"`
	Query           string `json:"query,omitempty"`
	TimeMin         string `json:"time_min,omitempty"`
	TimeMax         string `json:"time_max,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
	SingleEvents    *bool  `json:"single_events,omitempty"`
	IncludeDeleted  *bool  `json:"include_deleted,omitempty"`
	OrderBy         string `json:"order_by,omitempty"`
}

func NewGoogleCalendarQueryTool(store storage.Store) *GoogleCalendarQueryTool {
	return &GoogleCalendarQueryTool{
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *GoogleCalendarQueryTool) Name() string {
	return "google_calendar_query"
}

func (t *GoogleCalendarQueryTool) Description() string {
	return "Query Google Calendar integrations. Supports listing calendars and listing events."
}

func (t *GoogleCalendarQueryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "Operation to run: list_calendars or list_events",
				"enum":        []string{"list_calendars", "list_events"},
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific integration name to use (optional)",
			},
			"calendar_id": map[string]interface{}{
				"type":        "string",
				"description": "Calendar ID (defaults to integration calendar_id, then primary)",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Free-text search query for events",
			},
			"time_min": map[string]interface{}{
				"type":        "string",
				"description": "Lower bound (RFC3339) for event start time filtering",
			},
			"time_max": map[string]interface{}{
				"type":        "string",
				"description": "Upper bound (RFC3339) for event start time filtering",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum result count (1-250, default 20)",
			},
			"single_events": map[string]interface{}{
				"type":        "boolean",
				"description": "Expand recurring events into instances (default true)",
			},
			"include_deleted": map[string]interface{}{
				"type":        "boolean",
				"description": "Include deleted events (default false)",
			},
			"order_by": map[string]interface{}{
				"type":        "string",
				"description": "Event order. Supported by Google: startTime or updated",
				"enum":        []string{"startTime", "updated"},
			},
		},
		"required": []string{"operation"},
	}
}

func (t *GoogleCalendarQueryTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p GoogleCalendarQueryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	operation := strings.ToLower(strings.TrimSpace(p.Operation))
	if operation == "" {
		return &tools.Result{Success: false, Error: "operation is required"}, nil
	}
	if operation != "list_calendars" && operation != "list_events" {
		return &tools.Result{Success: false, Error: "unsupported operation; use list_calendars or list_events"}, nil
	}

	integration, err := t.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	accessToken, err := t.resolveAccessToken(ctx, integration)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	switch operation {
	case "list_calendars":
		payload, err := t.listCalendars(ctx, accessToken, p.MaxResults)
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		return &tools.Result{Success: true, Output: payload}, nil
	case "list_events":
		payload, err := t.listEvents(ctx, accessToken, integration, p)
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		return &tools.Result{Success: true, Output: payload}, nil
	default:
		return &tools.Result{Success: false, Error: "unknown operation"}, nil
	}
}

func (t *GoogleCalendarQueryTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "google_calendar" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled google_calendar integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("google_calendar integration with id %q not found or disabled", id)
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
			return nil, fmt.Errorf("multiple google_calendar integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("google_calendar integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple google_calendar integrations are enabled; pass integration_id or integration_name")
}

func (t *GoogleCalendarQueryTool) resolveAccessToken(ctx context.Context, integration *storage.Integration) (string, error) {
	if integration == nil {
		return "", fmt.Errorf("integration is required")
	}
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}

	accessToken := strings.TrimSpace(integration.Config["access_token"])
	tokenExpiry := strings.TrimSpace(integration.Config["token_expiry"])
	if accessToken != "" && tokenExpiry != "" {
		if expiry, err := time.Parse(time.RFC3339, tokenExpiry); err == nil {
			if expiry.After(time.Now().Add(30 * time.Second)) {
				return accessToken, nil
			}
		}
	}

	clientID := strings.TrimSpace(integration.Config["client_id"])
	clientSecret := strings.TrimSpace(integration.Config["client_secret"])
	refreshToken := strings.TrimSpace(integration.Config["refresh_token"])
	tokenURL := strings.TrimSpace(integration.Config["token_url"])
	if tokenURL == "" {
		tokenURL = defaultGoogleTokenURL
	}

	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return "", fmt.Errorf("integration %q is missing OAuth fields; need client_id, client_secret, refresh_token", integration.Name)
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read token refresh response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token refresh response: %w", err)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", fmt.Errorf("token refresh response did not include access_token")
	}

	integration.Config["access_token"] = tokenResp.AccessToken
	if tokenResp.ExpiresIn > 0 {
		integration.Config["token_expiry"] = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	integration.UpdatedAt = time.Now()
	if err := t.store.SaveIntegration(integration); err != nil {
		return "", fmt.Errorf("refreshed token but failed to persist integration: %w", err)
	}

	return tokenResp.AccessToken, nil
}

func (t *GoogleCalendarQueryTool) listCalendars(ctx context.Context, accessToken string, maxResults int) (string, error) {
	if maxResults <= 0 {
		maxResults = 100
	}
	if maxResults > 250 {
		maxResults = 250
	}

	query := url.Values{}
	query.Set("maxResults", strconv.Itoa(maxResults))
	reqURL := googleCalendarListEndpoint + "?" + query.Encode()

	var raw struct {
		Items []struct {
			ID          string `json:"id"`
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Primary     bool   `json:"primary"`
			AccessRole  string `json:"accessRole"`
			TimeZone    string `json:"timeZone"`
		} `json:"items"`
	}
	if err := t.doGoogleGet(ctx, reqURL, accessToken, &raw); err != nil {
		return "", err
	}

	type calendarItem struct {
		ID          string `json:"id"`
		Summary     string `json:"summary"`
		Description string `json:"description,omitempty"`
		Primary     bool   `json:"primary"`
		AccessRole  string `json:"access_role"`
		TimeZone    string `json:"time_zone,omitempty"`
	}
	out := struct {
		Operation string         `json:"operation"`
		Count     int            `json:"count"`
		Calendars []calendarItem `json:"calendars"`
	}{
		Operation: "list_calendars",
		Count:     len(raw.Items),
		Calendars: make([]calendarItem, 0, len(raw.Items)),
	}
	for _, item := range raw.Items {
		out.Calendars = append(out.Calendars, calendarItem{
			ID:          item.ID,
			Summary:     item.Summary,
			Description: item.Description,
			Primary:     item.Primary,
			AccessRole:  item.AccessRole,
			TimeZone:    item.TimeZone,
		})
	}

	return marshalIndented(out)
}

func (t *GoogleCalendarQueryTool) listEvents(ctx context.Context, accessToken string, integration *storage.Integration, p GoogleCalendarQueryParams) (string, error) {
	maxResults := p.MaxResults
	if maxResults <= 0 {
		maxResults = 20
	}
	if maxResults > 250 {
		maxResults = 250
	}

	calendarID := strings.TrimSpace(p.CalendarID)
	if calendarID == "" {
		calendarID = strings.TrimSpace(integration.Config["calendar_id"])
	}
	if calendarID == "" {
		calendarID = "primary"
	}

	query := url.Values{}
	query.Set("maxResults", strconv.Itoa(maxResults))
	if q := strings.TrimSpace(p.Query); q != "" {
		query.Set("q", q)
	}

	if strings.TrimSpace(p.TimeMin) != "" {
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(p.TimeMin)); err != nil {
			return "", fmt.Errorf("time_min must be RFC3339: %w", err)
		}
		query.Set("timeMin", strings.TrimSpace(p.TimeMin))
	}
	if strings.TrimSpace(p.TimeMax) != "" {
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(p.TimeMax)); err != nil {
			return "", fmt.Errorf("time_max must be RFC3339: %w", err)
		}
		query.Set("timeMax", strings.TrimSpace(p.TimeMax))
	}

	singleEvents := true
	if p.SingleEvents != nil {
		singleEvents = *p.SingleEvents
	}
	query.Set("singleEvents", strconv.FormatBool(singleEvents))

	includeDeleted := false
	if p.IncludeDeleted != nil {
		includeDeleted = *p.IncludeDeleted
	}
	query.Set("showDeleted", strconv.FormatBool(includeDeleted))

	if orderBy := strings.TrimSpace(p.OrderBy); orderBy != "" {
		if orderBy != "startTime" && orderBy != "updated" {
			return "", fmt.Errorf("order_by must be startTime or updated")
		}
		query.Set("orderBy", orderBy)
	} else if singleEvents {
		query.Set("orderBy", "startTime")
	}

	reqURL := fmt.Sprintf("%s/%s/events?%s", googleCalendarEventsBase, url.PathEscape(calendarID), query.Encode())

	var raw struct {
		Items []struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			Summary     string `json:"summary"`
			Description string `json:"description"`
			HTMLLink    string `json:"htmlLink"`
			Location    string `json:"location"`
			Creator     struct {
				Email string `json:"email"`
			} `json:"creator"`
			Organizer struct {
				Email string `json:"email"`
			} `json:"organizer"`
			Start struct {
				Date     string `json:"date"`
				DateTime string `json:"dateTime"`
				TimeZone string `json:"timeZone"`
			} `json:"start"`
			End struct {
				Date     string `json:"date"`
				DateTime string `json:"dateTime"`
				TimeZone string `json:"timeZone"`
			} `json:"end"`
		} `json:"items"`
	}
	if err := t.doGoogleGet(ctx, reqURL, accessToken, &raw); err != nil {
		return "", err
	}

	type eventTime struct {
		Date     string `json:"date,omitempty"`
		DateTime string `json:"date_time,omitempty"`
		TimeZone string `json:"time_zone,omitempty"`
	}
	type eventItem struct {
		ID          string    `json:"id"`
		Status      string    `json:"status"`
		Summary     string    `json:"summary"`
		Description string    `json:"description,omitempty"`
		HTMLLink    string    `json:"html_link,omitempty"`
		Location    string    `json:"location,omitempty"`
		Creator     string    `json:"creator,omitempty"`
		Organizer   string    `json:"organizer,omitempty"`
		Start       eventTime `json:"start"`
		End         eventTime `json:"end"`
	}
	out := struct {
		Operation  string      `json:"operation"`
		CalendarID string      `json:"calendar_id"`
		Count      int         `json:"count"`
		Events     []eventItem `json:"events"`
	}{
		Operation:  "list_events",
		CalendarID: calendarID,
		Count:      len(raw.Items),
		Events:     make([]eventItem, 0, len(raw.Items)),
	}
	for _, item := range raw.Items {
		out.Events = append(out.Events, eventItem{
			ID:          item.ID,
			Status:      item.Status,
			Summary:     item.Summary,
			Description: item.Description,
			HTMLLink:    item.HTMLLink,
			Location:    item.Location,
			Creator:     item.Creator.Email,
			Organizer:   item.Organizer.Email,
			Start: eventTime{
				Date:     item.Start.Date,
				DateTime: item.Start.DateTime,
				TimeZone: item.Start.TimeZone,
			},
			End: eventTime{
				Date:     item.End.Date,
				DateTime: item.End.DateTime,
				TimeZone: item.End.TimeZone,
			},
		})
	}

	return marshalIndented(out)
}

func (t *GoogleCalendarQueryTool) doGoogleGet(ctx context.Context, reqURL string, accessToken string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("google api returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

func marshalIndented(v interface{}) (string, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("failed to encode result: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

var _ tools.Tool = (*GoogleCalendarQueryTool)(nil)
