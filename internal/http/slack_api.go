package http

// Slack Web API client. Tokens travel only in the Authorization header, never in URLs or errors.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	slackDefaultAPIBaseURL  = "https://slack.com/api"
	slackMaxRateLimitTries  = 3
	slackMaxRetryAfter      = 30 * time.Second
	slackAPIRequestTimeout  = 30 * time.Second
	slackMaxAPIResponseSize = 16 << 20
	slackPageSize           = "200"
)

type slackAPIError struct {
	Method string
	Code   string
}

func (e *slackAPIError) Error() string {
	return e.Method + ": " + e.Code
}

func slackErrorCode(err error) string {
	var apiErr *slackAPIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// slackSafeError drops the request URL: for the Socket Mode dial it carries the connection ticket.
func slackSafeError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

type slackClient struct {
	baseURL string
	http    *http.Client
}

func (s *Server) slackAPI() *slackClient {
	baseURL := strings.TrimRight(s.slack.baseURL, "/")
	if baseURL == "" {
		baseURL = slackDefaultAPIBaseURL
	}
	return &slackClient{baseURL: baseURL, http: &http.Client{Timeout: slackAPIRequestTimeout}}
}

func (c *slackClient) call(ctx context.Context, token, method string, params url.Values, out interface{}) error {
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, strings.NewReader(params.Encode()))
		if err != nil {
			return fmt.Errorf("%s: failed to build request", method)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %w", method, slackSafeError(err))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, slackMaxAPIResponseSize))
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests && attempt < slackMaxRateLimitTries {
			if err := slackWaitRetryAfter(ctx, resp.Header.Get("Retry-After")); err != nil {
				return fmt.Errorf("%s: %w", method, err)
			}
			continue
		}
		if readErr != nil {
			return fmt.Errorf("%s: failed to read response: %w", method, readErr)
		}
		var status struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &status); err != nil {
			return fmt.Errorf("%s: unexpected response (HTTP %d)", method, resp.StatusCode)
		}
		if !status.OK {
			code := status.Error
			if code == "" {
				code = fmt.Sprintf("http_%d", resp.StatusCode)
			}
			return &slackAPIError{Method: method, Code: code}
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("%s: failed to decode response: %w", method, err)
		}
		return nil
	}
}

func slackWaitRetryAfter(ctx context.Context, header string) error {
	wait := time.Second
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds > 0 {
		wait = time.Duration(seconds) * time.Second
	}
	timer := time.NewTimer(min(wait, slackMaxRetryAfter))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type slackIdentity struct {
	TeamID string `json:"team_id"`
	Team   string `json:"team"`
	UserID string `json:"user_id"`
	User   string `json:"user"`
	BotID  string `json:"bot_id"`
}

func (c *slackClient) authTest(ctx context.Context, botToken string) (slackIdentity, error) {
	var identity slackIdentity
	err := c.call(ctx, botToken, "auth.test", url.Values{}, &identity)
	return identity, err
}

func (c *slackClient) openSocketURL(ctx context.Context, appToken string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.call(ctx, appToken, "apps.connections.open", url.Values{}, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("apps.connections.open: empty url")
	}
	return out.URL, nil
}

type slackUser struct {
	ID                string `json:"id"`
	TeamID            string `json:"team_id"`
	Name              string `json:"name"`
	RealName          string `json:"real_name"`
	Deleted           bool   `json:"deleted"`
	IsBot             bool   `json:"is_bot"`
	IsRestricted      bool   `json:"is_restricted"`
	IsUltraRestricted bool   `json:"is_ultra_restricted"`
	IsStranger        bool   `json:"is_stranger"`
	Profile           struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

func (u slackUser) displayName() string {
	for _, name := range []string{u.Profile.DisplayName, u.RealName, u.Profile.RealName, u.Name} {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return u.ID
}

func (c *slackClient) userInfo(ctx context.Context, botToken, userID string) (slackUser, error) {
	var out struct {
		User slackUser `json:"user"`
	}
	err := c.call(ctx, botToken, "users.info", url.Values{"user": {userID}}, &out)
	return out.User, err
}

type slackResponseMetadata struct {
	NextCursor string `json:"next_cursor"`
}

func (c *slackClient) listUsers(ctx context.Context, botToken string) ([]slackUser, error) {
	var users []slackUser
	cursor := ""
	for {
		params := url.Values{"limit": {slackPageSize}}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var page struct {
			Members          []slackUser           `json:"members"`
			ResponseMetadata slackResponseMetadata `json:"response_metadata"`
		}
		if err := c.call(ctx, botToken, "users.list", params, &page); err != nil {
			return nil, err
		}
		users = append(users, page.Members...)
		if cursor = page.ResponseMetadata.NextCursor; cursor == "" {
			return users, nil
		}
	}
}

type slackThreadMessage struct {
	TS         string `json:"ts"`
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	Username   string `json:"username"`
	Text       string `json:"text"`
	BotProfile struct {
		Name string `json:"name"`
	} `json:"bot_profile"`
}

// threadMessages returns the thread oldest first. oldest only narrows the fetch: Slack may still
// return the parent message, so callers filter by timestamp themselves.
func (c *slackClient) threadMessages(ctx context.Context, botToken, channel, threadTS, oldest string) ([]slackThreadMessage, error) {
	var messages []slackThreadMessage
	cursor := ""
	for {
		params := url.Values{"channel": {channel}, "ts": {threadTS}, "limit": {slackPageSize}}
		if oldest != "" {
			params.Set("oldest", oldest)
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var page struct {
			Messages         []slackThreadMessage  `json:"messages"`
			ResponseMetadata slackResponseMetadata `json:"response_metadata"`
		}
		if err := c.call(ctx, botToken, "conversations.replies", params, &page); err != nil {
			return nil, err
		}
		messages = append(messages, page.Messages...)
		if cursor = page.ResponseMetadata.NextCursor; cursor == "" {
			return messages, nil
		}
	}
}

// Unfurling is off because Slack fetches unfurled URLs by itself: an injected prompt could make
// the agent print a link carrying secrets, and that would leak them without anyone clicking.
func (c *slackClient) postMessage(ctx context.Context, botToken, channel, threadTS, text string) error {
	return c.call(ctx, botToken, "chat.postMessage", url.Values{
		"channel": {channel}, "thread_ts": {threadTS}, "text": {text},
		"unfurl_links": {"false"}, "unfurl_media": {"false"},
	}, nil)
}

func (c *slackClient) postEphemeral(ctx context.Context, botToken, channel, user, threadTS, text string) error {
	return c.call(ctx, botToken, "chat.postEphemeral", url.Values{"channel": {channel}, "user": {user}, "thread_ts": {threadTS}, "text": {text}}, nil)
}

func (c *slackClient) reaction(ctx context.Context, botToken, method, channel, ts, name string) error {
	return c.call(ctx, botToken, method, url.Values{"channel": {channel}, "timestamp": {ts}, "name": {name}}, nil)
}
