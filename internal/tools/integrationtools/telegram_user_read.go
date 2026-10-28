package integrationtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tguser"
	"github.com/A2gent/brute/internal/tools"
)

const (
	// TelegramUserProvider is the integration provider for user-account (MTProto) access.
	TelegramUserProvider = "telegram_user"

	telegramUserSessionKey  = "session"
	telegramUserAllowedKey  = "allowed_chats"
	telegramUserCursorsKey  = "cursors"
	telegramUserReadTimeout = 60 * time.Second
)

// TelegramUserCredentials extracts MTProto credentials from an integration.
func TelegramUserCredentials(integration *storage.Integration) (tguser.Credentials, error) {
	apiID, err := strconv.Atoi(strings.TrimSpace(integration.Config["api_id"]))
	if err != nil || apiID == 0 {
		return tguser.Credentials{}, fmt.Errorf("integration has invalid api_id")
	}
	creds := tguser.Credentials{APIID: apiID, APIHash: strings.TrimSpace(integration.Config["api_hash"])}
	if raw := strings.TrimSpace(integration.Config[telegramUserSessionKey]); raw != "" {
		data, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return creds, fmt.Errorf("stored session is corrupt: %w", err)
		}
		creds.Session = data
	}
	return creds, nil
}

// TelegramUserSetSession stores a session blob in the integration config.
func TelegramUserSetSession(integration *storage.Integration, session []byte) {
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}
	integration.Config[telegramUserSessionKey] = base64.StdEncoding.EncodeToString(session)
}

// TelegramUserAllowedChats returns the allowlisted chat ids.
func TelegramUserAllowedChats(integration *storage.Integration) map[string]struct{} {
	out := map[string]struct{}{}
	for _, id := range strings.FieldsFunc(integration.Config[telegramUserAllowedKey], func(r rune) bool {
		return r == ',' || r == '\n' || r == ' '
	}) {
		out[id] = struct{}{}
	}
	return out
}

func telegramUserCursors(integration *storage.Integration) map[string]int {
	cursors := map[string]int{}
	if raw := strings.TrimSpace(integration.Config[telegramUserCursorsKey]); raw != "" {
		_ = json.Unmarshal([]byte(raw), &cursors)
	}
	return cursors
}

func selectTelegramUserIntegration(ctx context.Context, store storage.Store, id string) (*storage.Integration, error) {
	all, err := listIntegrations(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}
	var candidates []*storage.Integration
	for _, item := range all {
		if item.Provider != TelegramUserProvider || !item.Enabled {
			continue
		}
		if id != "" && item.ID == id {
			return item, nil
		}
		candidates = append(candidates, item)
	}
	if id != "" {
		return nil, fmt.Errorf("telegram_user integration %q not found or disabled", id)
	}
	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("no enabled telegram_user integrations found")
	case 1:
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple telegram_user integrations are enabled; pass integration_id")
}

// persistTelegramUser re-reads the integration and saves session/cursor changes
// so concurrent edits of other fields are not clobbered.
func persistTelegramUser(store storage.Store, id string, session []byte, cursorUpdates map[string]int) {
	fresh, err := store.GetIntegration(id)
	if err != nil || fresh == nil {
		return
	}
	changed := false
	if len(session) > 0 {
		TelegramUserSetSession(fresh, session)
		changed = true
	}
	if len(cursorUpdates) > 0 {
		cursors := telegramUserCursors(fresh)
		for k, v := range cursorUpdates {
			if v > cursors[k] {
				cursors[k] = v
			}
		}
		raw, _ := json.Marshal(cursors)
		fresh.Config[telegramUserCursorsKey] = string(raw)
		changed = true
	}
	if changed {
		fresh.UpdatedAt = time.Now()
		_ = store.SaveIntegration(fresh)
	}
}

// TelegramUserListChatsTool lists allowlisted chats readable via the user account.
type TelegramUserListChatsTool struct{ store storage.Store }

func NewTelegramUserListChatsTool(store storage.Store) *TelegramUserListChatsTool {
	return &TelegramUserListChatsTool{store: store}
}

func (t *TelegramUserListChatsTool) Name() string { return "telegram_user_list_chats" }

func (t *TelegramUserListChatsTool) Description() string {
	return "List Telegram chats (groups and private conversations) the user has allowed the agent to read. Read-only."
}

func (t *TelegramUserListChatsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"integration_id": map[string]interface{}{"type": "string", "description": "telegram_user integration ID (optional if only one exists)"},
		},
	}
}

func (t *TelegramUserListChatsTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p struct {
		IntegrationID string `json:"integration_id"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}
	integration, err := selectTelegramUserIntegration(ctx, t.store, strings.TrimSpace(p.IntegrationID))
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	creds, err := TelegramUserCredentials(integration)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	allowed := TelegramUserAllowedChats(integration)
	if len(allowed) == 0 {
		return &tools.Result{Success: true, Output: "No chats are allowed yet. The user must select chats in the integration settings."}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, telegramUserReadTimeout)
	defer cancel()
	dialogs, sess, err := tguser.ListDialogs(ctx, creds)
	persistTelegramUser(t.store, integration.ID, sess, nil)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	var lines []string
	for _, d := range dialogs {
		if _, ok := allowed[d.ID]; ok {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s", d.ID, d.Kind, d.Title))
		}
	}
	sort.Strings(lines)
	return &tools.Result{Success: true, Output: "chat_id\tkind\ttitle\n" + strings.Join(lines, "\n")}, nil
}

// TelegramUserReadMessagesTool reads new messages from an allowlisted chat.
type TelegramUserReadMessagesTool struct{ store storage.Store }

func NewTelegramUserReadMessagesTool(store storage.Store) *TelegramUserReadMessagesTool {
	return &TelegramUserReadMessagesTool{store: store}
}

func (t *TelegramUserReadMessagesTool) Name() string { return "telegram_user_read_messages" }

func (t *TelegramUserReadMessagesTool) Description() string {
	return "Read messages from an allowed Telegram chat via the user's own account. Read-only. " +
		"By default returns only messages not yet seen by previous calls (a per-chat cursor is kept), " +
		"so a recurring loop gets just the new discussion each run."
}

func (t *TelegramUserReadMessagesTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"chat_id":        map[string]interface{}{"type": "string", "description": "Chat id from telegram_user_list_chats"},
			"integration_id": map[string]interface{}{"type": "string", "description": "telegram_user integration ID (optional if only one exists)"},
			"limit":          map[string]interface{}{"type": "integer", "description": "Max messages to return (default 200, max 500)"},
			"lookback_hours": map[string]interface{}{"type": "integer", "description": "On the first read of a chat (no cursor), only include messages from the last N hours (default 24)"},
			"advance_cursor": map[string]interface{}{"type": "boolean", "description": "Remember what was returned so the next call only gives newer messages (default true). Use false to peek."},
			"ignore_cursor":  map[string]interface{}{"type": "boolean", "description": "Re-read using lookback_hours even if a cursor exists (default false)"},
		},
		"required": []string{"chat_id"},
	}
}

func (t *TelegramUserReadMessagesTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p struct {
		ChatID        string `json:"chat_id"`
		IntegrationID string `json:"integration_id"`
		Limit         int    `json:"limit"`
		LookbackHours int    `json:"lookback_hours"`
		AdvanceCursor *bool  `json:"advance_cursor"`
		IgnoreCursor  bool   `json:"ignore_cursor"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	chatID := strings.TrimSpace(p.ChatID)
	if chatID == "" {
		return &tools.Result{Success: false, Error: "chat_id is required"}, nil
	}
	integration, err := selectTelegramUserIntegration(ctx, t.store, strings.TrimSpace(p.IntegrationID))
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	if _, ok := TelegramUserAllowedChats(integration)[chatID]; !ok {
		return &tools.Result{Success: false, Error: fmt.Sprintf("chat %s is not in the allowed list for this integration", chatID)}, nil
	}
	creds, err := TelegramUserCredentials(integration)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	minID := 0
	if !p.IgnoreCursor {
		minID = telegramUserCursors(integration)[chatID]
	}
	lookback := p.LookbackHours
	if lookback <= 0 {
		lookback = 24
	}

	ctx, cancel := context.WithTimeout(ctx, telegramUserReadTimeout)
	defer cancel()
	res, sess, err := tguser.ReadMessages(ctx, creds, chatID, minID, p.Limit)
	if err != nil {
		persistTelegramUser(t.store, integration.ID, sess, nil)
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	lastID := 0
	if n := len(res.Messages); n > 0 {
		lastID = res.Messages[n-1].ID
	}
	msgs := res.Messages
	if minID == 0 {
		cutoff := time.Now().Add(-time.Duration(lookback) * time.Hour)
		kept := make([]tguser.Message, 0, len(msgs))
		for _, m := range msgs {
			if !m.Date.Before(cutoff) {
				kept = append(kept, m)
			}
		}
		msgs = kept
	}

	var cursorUpdates map[string]int
	if (p.AdvanceCursor == nil || *p.AdvanceCursor) && lastID > 0 {
		// Advance past everything fetched (including old messages filtered by lookback).
		cursorUpdates = map[string]int{chatID: lastID}
	}
	persistTelegramUser(t.store, integration.ID, sess, cursorUpdates)

	if len(msgs) == 0 {
		return &tools.Result{Success: true, Output: "No new messages."}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d message(s) from chat %s", len(msgs), chatID)
	if res.Truncated {
		b.WriteString(" (older messages were skipped because of the limit)")
	}
	b.WriteString(":\n")
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s] %s: %s\n", m.Date.Local().Format("2006-01-02 15:04"), m.From, m.Text)
	}
	return &tools.Result{Success: true, Output: b.String()}, nil
}

var (
	_ tools.Tool = (*TelegramUserListChatsTool)(nil)
	_ tools.Tool = (*TelegramUserReadMessagesTool)(nil)
)
