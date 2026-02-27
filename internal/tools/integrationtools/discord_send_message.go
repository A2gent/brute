package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const discordAPIBase = "https://discord.com/api/v10"

// DiscordSendMessageTool sends a message to a Discord channel through an enabled Discord integration.
type DiscordSendMessageTool struct {
	store  storage.Store
	client *http.Client
}

type DiscordSendMessageParams struct {
	Content         string `json:"content"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	// ChannelID overrides the integration default channel.
	ChannelID string `json:"channel_id,omitempty"`
	// ThreadID sends the message into a specific thread inside the channel.
	ThreadID string `json:"thread_id,omitempty"`
}

func NewDiscordSendMessageTool(store storage.Store) *DiscordSendMessageTool {
	return &DiscordSendMessageTool{
		store: store,
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (d *DiscordSendMessageTool) Name() string {
	return "discord_send_message"
}

func (d *DiscordSendMessageTool) Description() string {
	return "Send a message to a Discord channel through an enabled Discord integration. Supports sending to a specific channel or thread."
}

func (d *DiscordSendMessageTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Message content to send (up to 2000 characters)",
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific Discord integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific Discord integration name to use (optional)",
			},
			"channel_id": map[string]interface{}{
				"type":        "string",
				"description": "Override destination channel ID. Defaults to integration default channel.",
			},
			"thread_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional thread ID to send the message into a specific thread within the channel.",
			},
		},
		"required": []string{"content"},
	}
}

func (d *DiscordSendMessageTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p DiscordSendMessageParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	content := strings.TrimSpace(p.Content)
	if content == "" {
		return &tools.Result{Success: false, Error: "content is required"}, nil
	}

	integration, err := d.selectDiscordIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	botToken := strings.TrimSpace(integration.Config["bot_token"])
	if botToken == "" {
		return &tools.Result{Success: false, Error: "selected discord integration is missing bot_token"}, nil
	}

	channelID := strings.TrimSpace(p.ChannelID)
	if channelID == "" {
		channelID = strings.TrimSpace(integration.Config["channel_id"])
	}
	if channelID == "" {
		channelID = strings.TrimSpace(integration.Config["default_channel_id"])
	}
	if channelID == "" {
		return &tools.Result{Success: false, Error: "channel_id is required (set integration channel_id or pass channel_id parameter)"}, nil
	}

	threadID := strings.TrimSpace(p.ThreadID)

	messageID, err := d.sendMessage(ctx, botToken, channelID, threadID, content)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("discord send failed: %v", err)}, nil
	}

	output := fmt.Sprintf("Discord message sent to channel %s", channelID)
	if threadID != "" {
		output = fmt.Sprintf("Discord message sent to channel %s thread %s", channelID, threadID)
	}

	return &tools.Result{
		Success: true,
		Output:  output,
		Metadata: map[string]interface{}{
			"message_id": messageID,
			"channel_id": channelID,
			"thread_id":  threadID,
		},
	}, nil
}

func (d *DiscordSendMessageTool) sendMessage(ctx context.Context, botToken, channelID, threadID, content string) (string, error) {
	// Truncate to Discord's limit of 2000 characters
	runes := []rune(content)
	if len(runes) > 2000 {
		content = string(runes[:2000])
	}

	payload := map[string]interface{}{
		"content": content,
	}
	if threadID != "" {
		payload["thread_id"] = threadID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to encode payload: %w", err)
	}

	url := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, channelID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		ID      string `json:"id"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &result)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(result.Message)
		if msg == "" {
			msg = string(raw)
		}
		return "", fmt.Errorf("discord API error %d: %s", resp.StatusCode, msg)
	}

	return result.ID, nil
}

func (d *DiscordSendMessageTool) selectDiscordIntegration(integrationID, integrationName string) (*storage.Integration, error) {
	all, err := d.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "discord" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled discord integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("discord integration with id %q not found or disabled", id)
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
			return nil, fmt.Errorf("multiple discord integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("discord integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple discord integrations are enabled; pass integration_id or integration_name")
}

// Ensure DiscordSendMessageTool implements Tool.
var _ tools.Tool = (*DiscordSendMessageTool)(nil)
