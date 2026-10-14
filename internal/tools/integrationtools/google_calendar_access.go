package integrationtools

// Google Calendar sign-in helpers and the per-integration calendar allow-list.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

const (
	// GoogleCalendarProvider is the integration provider id.
	GoogleCalendarProvider = "google_calendar"

	calendarScopeReadonly = "https://www.googleapis.com/auth/calendar.readonly"
	calendarScopeEvents   = "https://www.googleapis.com/auth/calendar.events"

	CalendarAccessRead  = "read"
	CalendarAccessWrite = "write"
)

// CalendarAccessLevel returns "write" (read + add events) or the read-only default.
func CalendarAccessLevel(integration *storage.Integration) string {
	if integration != nil && strings.EqualFold(strings.TrimSpace(integration.Config["access_level"]), CalendarAccessWrite) {
		return CalendarAccessWrite
	}
	return CalendarAccessRead
}

// GoogleCalendarScopes are the scopes requested at sign-in for the access level.
func GoogleCalendarScopes(level string) []string {
	scopes := []string{calendarScopeReadonly}
	if level == CalendarAccessWrite {
		scopes = append(scopes, calendarScopeEvents)
	}
	return scopes
}

// WritableCalendarIDs lists calendars the agent may add events to (always a subset of the allowed ones).
func WritableCalendarIDs(integration *storage.Integration) []string {
	allowed := AllowedCalendarIDs(integration)
	var out []string
	for _, part := range strings.FieldsFunc(integration.Config["writable_calendars"], func(r rune) bool { return r == ',' || r == '\n' }) {
		if id := strings.TrimSpace(part); id != "" && calendarAllowed(allowed, id) {
			out = append(out, id)
		}
	}
	return out
}

// CalendarInfo describes one calendar the signed-in account can see.
type CalendarInfo struct {
	ID              string `json:"id"`
	Summary         string `json:"summary"`
	Description     string `json:"description,omitempty"`
	Primary         bool   `json:"primary"`
	AccessRole      string `json:"access_role"`
	TimeZone        string `json:"time_zone,omitempty"`
	BackgroundColor string `json:"background_color,omitempty"`
}

// ListGoogleCalendars returns every calendar visible to the token.
func ListGoogleCalendars(ctx context.Context, client *http.Client, accessToken string) ([]CalendarInfo, error) {
	t := &GoogleCalendarQueryTool{client: client}
	var raw struct {
		Items []struct {
			ID              string `json:"id"`
			Summary         string `json:"summary"`
			SummaryOverride string `json:"summaryOverride"`
			Description     string `json:"description"`
			Primary         bool   `json:"primary"`
			AccessRole      string `json:"accessRole"`
			TimeZone        string `json:"timeZone"`
			BackgroundColor string `json:"backgroundColor"`
		} `json:"items"`
	}
	reqURL := googleCalendarListEndpoint + "?" + url.Values{"maxResults": {"250"}}.Encode()
	if err := t.doGoogleGet(ctx, reqURL, accessToken, &raw); err != nil {
		return nil, err
	}
	out := make([]CalendarInfo, 0, len(raw.Items))
	for _, item := range raw.Items {
		name := item.SummaryOverride
		if name == "" {
			name = item.Summary
		}
		out = append(out, CalendarInfo{
			ID: item.ID, Summary: name, Description: item.Description, Primary: item.Primary,
			AccessRole: item.AccessRole, TimeZone: item.TimeZone, BackgroundColor: item.BackgroundColor,
		})
	}
	return out, nil
}

// ListCalendarsForIntegration refreshes the token if needed and lists calendars (used by the picker).
func ListCalendarsForIntegration(ctx context.Context, store storage.Store, integration *storage.Integration) ([]CalendarInfo, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	token, err := GoogleAccessToken(ctx, store, client, integration)
	if err != nil {
		return nil, err
	}
	return ListGoogleCalendars(ctx, client, token)
}

func fetchCalendarOwner(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	cals, err := ListGoogleCalendars(ctx, client, accessToken)
	if err != nil {
		return "", err
	}
	for _, c := range cals {
		if c.Primary {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("no primary calendar found for this account")
}

// AllowedCalendarIDs returns the calendars the agent may read. Empty means "all calendars"
// (integrations created before the picker existed keep working).
func AllowedCalendarIDs(integration *storage.Integration) []string {
	var out []string
	for _, part := range strings.FieldsFunc(integration.Config["allowed_calendars"], func(r rune) bool { return r == ',' || r == '\n' }) {
		if id := strings.TrimSpace(part); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func calendarAllowed(allowed []string, id string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == id {
			return true
		}
	}
	return false
}
