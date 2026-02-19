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

const telegramSendMessageEndpointFmt = "https://api.telegram.org/bot%s/sendMessage"

// TelegramSendMessageTool sends a message using configured Telegram integrations.
type TelegramSendMessageTool struct {
	store  storage.Store
	client *http.Client
}

type TelegramSendMessageParams struct {
	Text            string `json:"text"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	ChatID          string `json:"chat_id,omitempty"`
	MessageThreadID int64  `json:"message_thread_id,omitempty"`
}

func NewTelegramSendMessageTool(store storage.Store) *TelegramSendMessageTool {
	return &TelegramSendMessageTool{
		store: store,
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (t *TelegramSendMessageTool) Name() string {
	return "telegram_send_message"
}

func (t *TelegramSendMessageTool) Description() string {
	return "Send a Telegram message through an enabled Telegram integration."
}

func (t *TelegramSendMessageTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Message text to send",
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific Telegram integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific Telegram integration name to use (optional)",
			},
			"chat_id": map[string]interface{}{
				"type":        "string",
				"description": "Override destination chat ID. Defaults to integration chat_id.",
			},
			"message_thread_id": map[string]interface{}{
				"type":        "integer",
				"description": "Optional topic/thread ID inside the destination chat",
			},
		},
		"required": []string{"text"},
	}
}

func (t *TelegramSendMessageTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p TelegramSendMessageParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}

	integration, err := t.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	botToken := strings.TrimSpace(integration.Config["bot_token"])
	if botToken == "" {
		return &tools.Result{Success: false, Error: "selected telegram integration is missing bot_token"}, nil
	}

	chatID := strings.TrimSpace(p.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(integration.Config["chat_id"])
	}
	if chatID == "" {
		chatID = strings.TrimSpace(integration.Config["default_chat_id"])
	}
	if chatID == "" {
		return &tools.Result{Success: false, Error: "chat_id is required (set integration default_chat_id/chat_id or pass chat_id parameter)"}, nil
	}

	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if p.MessageThreadID > 0 {
		payload["message_thread_id"] = p.MessageThreadID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Telegram payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf(telegramSendMessageEndpointFmt, botToken),
		bytes.NewReader(body),
	)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create Telegram request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("telegram request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read Telegram response: %v", err)}, nil
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to decode Telegram response: %v", err)}, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		if msg == "" {
			msg = resp.Status
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("telegram sendMessage failed: %s", msg)}, nil
	}

	return &tools.Result{
		Success: true,
		Output:  fmt.Sprintf("Telegram message sent to chat %s", chatID),
	}, nil
}

func (t *TelegramSendMessageTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "telegram" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled telegram integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("telegram integration with id %q not found or disabled", id)
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
			return nil, fmt.Errorf("multiple telegram integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("telegram integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple telegram integrations are enabled; pass integration_id or integration_name")
}

// Ensure TelegramSendMessageTool implements Tool.
var _ tools.Tool = (*TelegramSendMessageTool)(nil)
