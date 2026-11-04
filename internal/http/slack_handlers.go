package http

// Slack integration validation, connectivity test and the member picker endpoint.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/storage"
)

const (
	slackBotTokenPrefix = "xoxb-"
	slackAppTokenPrefix = "xapp-"
	slackbotUserID      = "USLACKBOT"
)

func normalizeSlackConfig(integration *storage.Integration) {
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}
	mode := strings.ToLower(strings.TrimSpace(integration.Config["access_mode"]))
	if mode == "" {
		mode = slackAccessModeAllowlist
	}
	integration.Config["access_mode"] = mode
}

func validateSlackIntegration(integration storage.Integration) error {
	botToken := strings.TrimSpace(integration.Config["bot_token"])
	appToken := strings.TrimSpace(integration.Config["app_token"])

	if strings.HasPrefix(botToken, slackAppTokenPrefix) {
		return errors.New("bot_token looks like an app-level token (xapp-): put the Bot User OAuth Token (xoxb-) in bot_token and the xapp- token in app_token")
	}
	if !strings.HasPrefix(botToken, slackBotTokenPrefix) {
		return errors.New("bot_token must start with xoxb-: use the Bot User OAuth Token from OAuth & Permissions")
	}
	switch integration.Mode {
	case "duplex":
		if appToken == "" {
			return errors.New("missing required config field: app_token")
		}
	case "notify_only":
		if strings.TrimSpace(integration.Config["channel_id"]) == "" {
			return errors.New("missing required config field: channel_id")
		}
	}
	if appToken != "" {
		if strings.HasPrefix(appToken, slackBotTokenPrefix) {
			return errors.New("app_token looks like a bot token (xoxb-): put the app-level token (xapp-) in app_token and the xoxb- token in bot_token")
		}
		if !strings.HasPrefix(appToken, slackAppTokenPrefix) {
			return errors.New("app_token must start with xapp-: create an app-level token under Basic Information")
		}
	}
	switch strings.ToLower(strings.TrimSpace(integration.Config["access_mode"])) {
	case "", slackAccessModeAllowlist, slackAccessModeWorkspace:
		return nil
	default:
		return errors.New("access_mode must be allowlist or workspace")
	}
}

func (s *Server) testSlackIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	api := s.slackAPI()
	identity, err := api.authTest(ctx, strings.TrimSpace(integration.Config["bot_token"]))
	if err != nil {
		return false, fmt.Sprintf("Slack bot token check failed (%v). Copy the Bot User OAuth Token (xoxb-) from OAuth & Permissions, and reinstall the app if it was revoked.", err)
	}
	if appToken := strings.TrimSpace(integration.Config["app_token"]); appToken != "" {
		if _, err := api.openSocketURL(ctx, appToken); err != nil {
			return false, slackAppTokenFailure(err)
		}
	}
	return true, fmt.Sprintf("Connected to %s as @%s", identity.Team, identity.User)
}

func slackAppTokenFailure(err error) string {
	switch slackErrorCode(err) {
	case "missing_scope":
		return fmt.Sprintf("Slack app token check failed (%v). The app token is missing the connections:write scope: create a new app-level token with it under Basic Information.", err)
	case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive", "not_allowed_token_type":
		return fmt.Sprintf("Slack app token check failed (%v). The app token was rejected: generate a new app-level token (xapp-) under Basic Information.", err)
	default:
		return fmt.Sprintf("Slack app token check failed (%v). Check that Socket Mode is turned on in the app settings and that the app token has the connections:write scope.", err)
	}
}

type SlackUsersRequest struct {
	BotToken      string `json:"bot_token"`
	IntegrationID string `json:"integration_id"`
}

type SlackUserResponse struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
}

type SlackUsersResponse struct {
	Users []SlackUserResponse `json:"users"`
}

// resolveSlackBotToken looks up the stored token when the form holds the mask. The provider check
// stops this endpoint from sending another integration's bot_token to Slack.
func (s *Server) resolveSlackBotToken(req SlackUsersRequest) string {
	botToken := strings.TrimSpace(req.BotToken)
	if botToken != "" && botToken != "***" {
		return botToken
	}
	if id := strings.TrimSpace(req.IntegrationID); id != "" {
		if integration, err := s.store.GetIntegration(id); err == nil && integration != nil && integration.Provider == "slack" {
			return strings.TrimSpace(integration.Config["bot_token"])
		}
	}
	return ""
}

func (s *Server) handleSlackUsers(w http.ResponseWriter, r *http.Request) {
	var req SlackUsersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	botToken := s.resolveSlackBotToken(req)
	if botToken == "" {
		s.errorResponse(w, http.StatusBadRequest, "bot_token is required")
		return
	}
	members, err := s.slackAPI().listUsers(r.Context(), botToken)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to list Slack users: "+err.Error())
		return
	}
	users := make([]SlackUserResponse, 0, len(members))
	for _, member := range members {
		if member.Deleted || member.IsBot || member.ID == slackbotUserID {
			continue
		}
		realName := member.RealName
		if realName == "" {
			realName = member.Profile.RealName
		}
		users = append(users, SlackUserResponse{ID: member.ID, Name: member.Name, RealName: realName})
	}
	s.jsonResponse(w, http.StatusOK, SlackUsersResponse{Users: users})
}
