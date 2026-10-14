package http

// Google integrations (Gmail, Google Calendar): "Sign in with Google" (loopback OAuth + PKCE),
// connection status, calendar picker and test. API access lives in tools/integrationtools/.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools/integrationtools"
	"github.com/go-chi/chi/v5"
)

const gmailOAuthTTL = 10 * time.Minute

type gmailOAuthPending struct {
	integrationID string
	redirectURI   string
	verifier      string
	expires       time.Time
}

var gmailOAuthStates = struct {
	sync.Mutex
	byState map[string]gmailOAuthPending
}{byState: map[string]gmailOAuthPending{}}

func putGmailOAuthState(p gmailOAuthPending) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := hex.EncodeToString(buf)
	gmailOAuthStates.Lock()
	defer gmailOAuthStates.Unlock()
	now := time.Now()
	for k, v := range gmailOAuthStates.byState {
		if now.After(v.expires) {
			delete(gmailOAuthStates.byState, k)
		}
	}
	p.expires = now.Add(gmailOAuthTTL)
	gmailOAuthStates.byState[state] = p
	return state, nil
}

func takeGmailOAuthState(state string) (gmailOAuthPending, bool) {
	gmailOAuthStates.Lock()
	defer gmailOAuthStates.Unlock()
	p, ok := gmailOAuthStates.byState[state]
	delete(gmailOAuthStates.byState, state)
	if !ok || time.Now().After(p.expires) {
		return gmailOAuthPending{}, false
	}
	return p, true
}

func (s *Server) googleIntegration(w http.ResponseWriter, r *http.Request) *storage.Integration {
	integration, err := s.store.GetIntegration(chi.URLParam(r, "integrationID"))
	if err != nil || integration == nil {
		s.errorResponse(w, http.StatusNotFound, "Integration not found")
		return nil
	}
	if !isGoogleOAuthProvider(integration.Provider) {
		s.errorResponse(w, http.StatusBadRequest, "Integration does not use Google sign-in")
		return nil
	}
	return integration
}

func isGoogleOAuthProvider(provider string) bool {
	return provider == integrationtools.GmailProvider || provider == integrationtools.GoogleCalendarProvider
}

// googleScopes returns the scopes to request for the integration's provider and access level.
func googleScopes(integration *storage.Integration) []string {
	if integration.Provider == integrationtools.GoogleCalendarProvider {
		return integrationtools.GoogleCalendarScopes(integrationtools.CalendarAccessLevel(integration))
	}
	return integrationtools.GmailScopes(integrationtools.GmailAccessLevel(integration))
}

// googleOAuthKeys are written by the sign-in flow and never edited through the settings form.
var googleOAuthKeys = []string{"refresh_token", "access_token", "token_expiry", "email", "granted_scopes"}

// applyGoogleClientReuse copies the OAuth client of another Google integration into req, so one
// Google Cloud app can serve Gmail and Calendar without retyping the secret.
func (s *Server) applyGoogleClientReuse(req *IntegrationRequest) error {
	if req.Config == nil {
		return nil
	}
	from := strings.TrimSpace(req.Config["reuse_client_from"])
	delete(req.Config, "reuse_client_from")
	if from == "" {
		return nil
	}
	src, err := s.store.GetIntegration(from)
	if err != nil || src == nil || !isGoogleOAuthProvider(src.Provider) {
		return fmt.Errorf("integration to reuse the Google app from was not found")
	}
	if src.Config["client_id"] == "" || src.Config["client_secret"] == "" {
		return fmt.Errorf("the selected integration has no OAuth client saved")
	}
	req.Config["client_id"] = src.Config["client_id"]
	req.Config["client_secret"] = src.Config["client_secret"]
	return nil
}

// handleGmailOAuthStart returns the Google consent URL for the saved OAuth client.
func (s *Server) handleGmailOAuthStart(w http.ResponseWriter, r *http.Request) {
	integration := s.googleIntegration(w, r)
	if integration == nil {
		return
	}
	clientID := strings.TrimSpace(integration.Config["client_id"])
	if clientID == "" || strings.TrimSpace(integration.Config["client_secret"]) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Save the OAuth client ID and secret first")
		return
	}
	verifier, challenge, err := integrationtools.NewPKCE()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create PKCE challenge: "+err.Error())
		return
	}
	redirectURI := "http://" + r.Host + "/integrations/gmail/oauth/callback"
	state, err := putGmailOAuthState(gmailOAuthPending{integrationID: integration.ID, redirectURI: redirectURI, verifier: verifier})
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create state: "+err.Error())
		return
	}
	authURL := integrationtools.GoogleAuthURL(clientID, redirectURI, state, challenge, googleScopes(integration))
	s.jsonResponse(w, http.StatusOK, map[string]string{"auth_url": authURL, "redirect_uri": redirectURI})
}

// handleGmailOAuthCallback is the browser landing page Google redirects to.
func (s *Server) handleGmailOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pending, ok := takeGmailOAuthState(q.Get("state"))
	if !ok {
		gmailCallbackPage(w, http.StatusBadRequest, false, "This sign-in link expired or was already used. Go back to Caesar and press \"Sign in with Google\" again.")
		return
	}
	if errCode := q.Get("error"); errCode != "" {
		gmailCallbackPage(w, http.StatusBadRequest, false, "Google reported: "+errCode)
		return
	}
	integration, err := s.store.GetIntegration(pending.integrationID)
	if err != nil || integration == nil {
		gmailCallbackPage(w, http.StatusNotFound, false, "The Gmail integration no longer exists.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := integrationtools.ExchangeGoogleCode(ctx, integration, q.Get("code"), pending.redirectURI, pending.verifier); err != nil {
		gmailCallbackPage(w, http.StatusBadGateway, false, err.Error())
		return
	}
	if err := integrationtools.FillGoogleAccountEmail(ctx, integration); err != nil {
		gmailCallbackPage(w, http.StatusBadGateway, false, err.Error())
		return
	}
	integration.UpdatedAt = time.Now()
	if err := s.store.SaveIntegration(integration); err != nil {
		gmailCallbackPage(w, http.StatusInternalServerError, false, "Failed to save tokens: "+err.Error())
		return
	}
	gmailCallbackPage(w, http.StatusOK, true, "Connected as "+integration.Config["email"]+". You can close this tab and return to Caesar.")
}

func gmailCallbackPage(w http.ResponseWriter, status int, ok bool, message string) {
	title := "Google account connected"
	if !ok {
		title = "Google connection failed"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%s</title>
<body style="font-family:system-ui;max-width:32rem;margin:4rem auto;padding:0 1rem"><h2>%s</h2><p>%s</p></body>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}

type gmailStatusResponse struct {
	Connected      bool   `json:"connected"`
	Email          string `json:"email,omitempty"`
	AccessLevel    string `json:"access_level,omitempty"`
	NeedsReconnect bool   `json:"needs_reconnect"`
}

// handleGmailStatus is polled by Caesar while the user signs in.
func (s *Server) handleGmailStatus(w http.ResponseWriter, r *http.Request) {
	integration := s.googleIntegration(w, r)
	if integration == nil {
		return
	}
	level := integrationtools.GmailAccessLevel(integration)
	resp := gmailStatusResponse{
		Connected: strings.TrimSpace(integration.Config["refresh_token"]) != "",
		Email:     integration.Config["email"],
	}
	if integration.Provider == integrationtools.GmailProvider {
		resp.AccessLevel = level
	}
	if integration.Provider == integrationtools.GoogleCalendarProvider {
		resp.AccessLevel = integrationtools.CalendarAccessLevel(integration)
		if resp.Connected && resp.AccessLevel == integrationtools.CalendarAccessWrite &&
			!strings.Contains(integration.Config["granted_scopes"], "calendar.events") {
			resp.NeedsReconnect = true
		}
	}
	// Raising the access level after sign-in needs a new consent for the compose scope.
	if resp.Connected && integration.Provider == integrationtools.GmailProvider && level == integrationtools.GmailAccessDraft &&
		!strings.Contains(integration.Config["granted_scopes"], "gmail.compose") {
		resp.NeedsReconnect = true
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) testGmailIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	msg, err := integrationtools.GmailTestSummary(ctx, s.store, integration)
	if err != nil {
		return false, err.Error()
	}
	return true, msg
}

type googleCalendarListResponse struct {
	Calendars []integrationtools.CalendarInfo `json:"calendars"`
}

// handleGoogleCalendarList feeds the calendar picker with every calendar of the signed-in account.
func (s *Server) handleGoogleCalendarList(w http.ResponseWriter, r *http.Request) {
	integration := s.googleIntegration(w, r)
	if integration == nil {
		return
	}
	if integration.Provider != integrationtools.GoogleCalendarProvider {
		s.errorResponse(w, http.StatusBadRequest, "Integration is not a google_calendar integration")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cals, err := integrationtools.ListCalendarsForIntegration(ctx, s.store, integration)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, googleCalendarListResponse{Calendars: cals})
}

func (s *Server) testGoogleCalendarIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cals, err := integrationtools.ListCalendarsForIntegration(ctx, s.store, integration)
	if err != nil {
		return false, err.Error()
	}
	allowed := integrationtools.AllowedCalendarIDs(integration)
	shared := len(cals)
	if len(allowed) > 0 {
		shared = len(allowed)
	}
	msg := fmt.Sprintf("Connected as %s. %d calendars in the account, %d shared with the agent.", integration.Config["email"], len(cals), shared)
	if integrationtools.CalendarAccessLevel(integration) == integrationtools.CalendarAccessWrite {
		msg += fmt.Sprintf(" The agent can add events to %d.", len(integrationtools.WritableCalendarIDs(integration)))
	} else {
		msg += " Read-only."
	}
	return true, msg
}
