package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// GoogleCalendarCreateEventTool adds events to calendars the user marked as writable.
// It never sends invitations and cannot edit or delete existing events.
type GoogleCalendarCreateEventTool struct {
	store  storage.Store
	client *http.Client
}

type GoogleCalendarCreateEventParams struct {
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	CalendarID      string `json:"calendar_id,omitempty"`
	Summary         string `json:"summary"`
	Description     string `json:"description,omitempty"`
	Location        string `json:"location,omitempty"`
	Start           string `json:"start,omitempty"`
	End             string `json:"end,omitempty"`
	StartDate       string `json:"start_date,omitempty"`
	EndDate         string `json:"end_date,omitempty"`
	TimeZone        string `json:"time_zone,omitempty"`
}

func NewGoogleCalendarCreateEventTool(store storage.Store) *GoogleCalendarCreateEventTool {
	return &GoogleCalendarCreateEventTool{store: store, client: &http.Client{Timeout: 30 * time.Second}}
}

func (t *GoogleCalendarCreateEventTool) Name() string { return "google_calendar_create_event" }

func (t *GoogleCalendarCreateEventTool) Description() string {
	return "Add an event to Google Calendar. Works only on calendars the user marked as writable and only when the integration has 'Read + add events' access. Timed events use start/end (RFC3339); all-day events use start_date/end_date (YYYY-MM-DD, end exclusive). Does not invite attendees and cannot modify or delete events."
}

func (t *GoogleCalendarCreateEventTool) Schema() map[string]interface{} {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"integration_id":   str("Google Calendar integration ID (optional if only one)"),
			"integration_name": str("Google Calendar integration name (optional)"),
			"calendar_id":      str("Calendar to add the event to (defaults to the first writable calendar)"),
			"summary":          str("Event title"),
			"description":      str("Event description"),
			"location":         str("Event location"),
			"start":            str("Start time, RFC3339 with offset, e.g. 2026-10-14T15:00:00+02:00"),
			"end":              str("End time, RFC3339. Defaults to start + 1 hour"),
			"start_date":       str("All-day event start date, YYYY-MM-DD"),
			"end_date":         str("All-day event end date (exclusive), YYYY-MM-DD. Defaults to start_date + 1 day"),
			"time_zone":        str("IANA time zone name, only needed when start has no offset"),
		},
		"required": []string{"summary"},
	}
}

func (t *GoogleCalendarCreateEventTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p GoogleCalendarCreateEventParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	fail := func(msg string) (*tools.Result, error) { return &tools.Result{Success: false, Error: msg}, nil }

	if strings.TrimSpace(p.Summary) == "" {
		return fail("summary is required")
	}
	startField, endField, err := buildEventTimes(p)
	if err != nil {
		return fail(err.Error())
	}

	integration, err := selectIntegrationByProvider(ctx, t.store, GoogleCalendarProvider, p.IntegrationID, p.IntegrationName)
	if err != nil {
		return fail(err.Error())
	}
	if CalendarAccessLevel(integration) != CalendarAccessWrite {
		return fail(fmt.Sprintf("google_calendar integration %q is read-only; switch its access level to \"Read + add events\" in Caesar and sign in again", integration.Name))
	}
	writable := WritableCalendarIDs(integration)
	if len(writable) == 0 {
		return fail(fmt.Sprintf("no calendar of integration %q is marked as writable; tick \"Agent can add events\" for a calendar in Caesar", integration.Name))
	}
	calendarID := strings.TrimSpace(p.CalendarID)
	if calendarID == "" {
		calendarID = writable[0]
	} else if !calendarAllowed(writable, calendarID) {
		return fail(fmt.Sprintf("calendar %q is not writable for the agent; writable calendars: %s", calendarID, strings.Join(writable, ", ")))
	}

	token, err := GoogleAccessToken(ctx, t.store, t.client, integration)
	if err != nil {
		return fail(err.Error())
	}

	event := map[string]interface{}{"summary": p.Summary, "start": startField, "end": endField}
	if p.Description != "" {
		event["description"] = p.Description
	}
	if p.Location != "" {
		event["location"] = p.Location
	}
	body, _ := json.Marshal(event)
	reqURL := fmt.Sprintf("%s/%s/events?sendUpdates=none", googleCalendarEventsBase, url.PathEscape(calendarID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return fail(err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return fail("request failed: " + err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fail(fmt.Sprintf("google api returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
	}
	var created struct {
		ID       string `json:"id"`
		HTMLLink string `json:"htmlLink"`
	}
	_ = json.Unmarshal(raw, &created)
	out, err := marshalIndented(map[string]interface{}{
		"event_id":    created.ID,
		"html_link":   created.HTMLLink,
		"calendar_id": calendarID,
		"summary":     p.Summary,
		"start":       startField,
		"end":         endField,
	})
	if err != nil {
		return fail(err.Error())
	}
	return &tools.Result{Success: true, Output: out}, nil
}

func buildEventTimes(p GoogleCalendarCreateEventParams) (map[string]string, map[string]string, error) {
	if sd := strings.TrimSpace(p.StartDate); sd != "" {
		start, err := time.Parse("2006-01-02", sd)
		if err != nil {
			return nil, nil, fmt.Errorf("start_date must be YYYY-MM-DD")
		}
		end := start.AddDate(0, 0, 1)
		if ed := strings.TrimSpace(p.EndDate); ed != "" {
			if end, err = time.Parse("2006-01-02", ed); err != nil {
				return nil, nil, fmt.Errorf("end_date must be YYYY-MM-DD")
			}
			if !end.After(start) {
				return nil, nil, fmt.Errorf("end_date must be after start_date")
			}
		}
		return map[string]string{"date": sd}, map[string]string{"date": end.Format("2006-01-02")}, nil
	}
	if strings.TrimSpace(p.Start) == "" {
		return nil, nil, fmt.Errorf("start (or start_date for all-day events) is required")
	}
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(p.Start))
	if err != nil {
		return nil, nil, fmt.Errorf("start must be RFC3339: %w", err)
	}
	end := start.Add(time.Hour)
	if e := strings.TrimSpace(p.End); e != "" {
		if end, err = time.Parse(time.RFC3339, e); err != nil {
			return nil, nil, fmt.Errorf("end must be RFC3339: %w", err)
		}
		if !end.After(start) {
			return nil, nil, fmt.Errorf("end must be after start")
		}
	}
	startField := map[string]string{"dateTime": start.Format(time.RFC3339)}
	endField := map[string]string{"dateTime": end.Format(time.RFC3339)}
	if tz := strings.TrimSpace(p.TimeZone); tz != "" {
		startField["timeZone"], endField["timeZone"] = tz, tz
	}
	return startField, endField, nil
}

// selectIntegrationByProvider mirrors SelectGmailIntegration for other providers.
func selectIntegrationByProvider(ctx context.Context, store storage.Store, provider, id, name string) (*storage.Integration, error) {
	all, err := listIntegrations(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}
	var candidates []*storage.Integration
	for _, item := range all {
		if item.Provider == provider && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled %s integrations found", provider)
	}
	if id = strings.TrimSpace(id); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("%s integration with id %q not found or disabled", provider, id)
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
			return nil, fmt.Errorf("%s integration named %q not found", provider, name)
		default:
			return nil, fmt.Errorf("multiple %s integrations matched name %q; pass integration_id", provider, name)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple %s integrations are enabled; pass integration_id or integration_name", provider)
}

var _ tools.Tool = (*GoogleCalendarCreateEventTool)(nil)
