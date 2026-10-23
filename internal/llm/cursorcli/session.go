package cursorcli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

// Cursor requires persistent-session chat IDs to be UUIDs; checking the shape
// also keeps the ID safe to use in a filesystem glob.
var cursorChatIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// cursorChatsDir is where Cursor Agent CLI stores chats as <md5(workspace)>/<chatId>.
var cursorChatsDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".cursor", "chats")
}

var resolveLoginAccessToken = ResolveAccessToken

// sessionCursor encodes the chat ID with the scope it was created in, so a later
// request from another account, workspace or model starts a new chat.
func (c *Client) sessionCursor(model, chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if !cursorChatIDRe.MatchString(chatID) {
		return ""
	}
	return c.sessionScope(model) + ":" + chatID
}

// resumeChatID returns the Cursor chat to resume, or "" when the request must
// start a fresh chat with the full transcript.
func (c *Client) resumeChatID(model, stored string) string {
	stored = strings.TrimSpace(stored)
	// The agent binds the cursor to its provider identity as "identity|cursor".
	if i := strings.LastIndex(stored, "|"); i >= 0 {
		stored = stored[i+1:]
	}
	scope, chatID, ok := strings.Cut(stored, ":")
	if !ok || !cursorChatIDRe.MatchString(chatID) || scope != c.sessionScope(model) {
		return ""
	}
	if !cursorChatExists(chatID) {
		logging.Debug("Cursor chat %s not found locally; starting a fresh chat", chatID)
		return ""
	}
	return chatID
}

func (c *Client) sessionScope(model string) string {
	return shortHash(c.accountFingerprint() + "|" + c.options.WorkDir + "|" + model)
}

func (c *Client) accountFingerprint() string {
	c.accountOnce.Do(func() {
		if c.options.APIKey != "" {
			c.account = "key:" + shortHash(c.options.APIKey)
			return
		}
		token, err := resolveLoginAccessToken()
		if err != nil || strings.TrimSpace(token) == "" {
			c.account = "login"
			return
		}
		// Prefer the JWT subject: access tokens rotate, the account does not.
		if sub := jwtSubject(token); sub != "" {
			c.account = "sub:" + shortHash(sub)
			return
		}
		c.account = "token:" + shortHash(token)
	})
	return c.account
}

// cursorChatExists guards --resume: Cursor silently creates an empty chat for an
// unknown UUID, which would drop the transcript the agent no longer sends.
func cursorChatExists(chatID string) bool {
	dir := cursorChatsDir()
	if dir == "" {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*", chatID))
	return len(matches) > 0
}

// promptFor builds the -p payload: only the new messages when resuming, otherwise
// the full transcript (rebuilt without the agent's cursor cut when available).
func promptFor(request *llm.ChatRequest, resumeID string, exposeA2gentMCP bool) string {
	if resumeID != "" {
		return buildResumePrompt(request.Messages)
	}
	if request.ProviderSessionCursor != "" && request.FullMessages != nil {
		full := *request
		full.Messages = request.FullMessages()
		return buildPrompt(&full, exposeA2gentMCP)
	}
	return buildPrompt(request, exposeA2gentMCP)
}

// buildResumePrompt omits the system prompt and transcript header: the resumed
// Cursor chat already holds both.
func buildResumePrompt(messages []llm.Message) string {
	if len(messages) == 0 {
		return "Continue."
	}
	if len(messages) == 1 && isPlainUserMessage(messages[0]) {
		return strings.TrimSpace(messages[0].Content)
	}
	var b strings.Builder
	for _, msg := range messages {
		writeMessage(&b, msg)
	}
	return strings.TrimSpace(b.String())
}

func isPlainUserMessage(msg llm.Message) bool {
	return msg.Role == "user" && len(msg.ToolCalls) == 0 && len(msg.ToolResults) == 0 && len(msg.Images) == 0
}

func isResumeError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "resume") ||
		strings.Contains(lower, "persistent session") ||
		strings.Contains(lower, "persistent-session") ||
		(strings.Contains(lower, "chat") && strings.Contains(lower, "not found"))
}

func jwtSubject(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Sub)
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
