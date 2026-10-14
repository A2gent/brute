package integrationtools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// GmailDraftTool saves draft emails in Gmail. It never sends: drafts wait for the user.
type GmailDraftTool struct {
	store  storage.Store
	client *http.Client
}

type GmailDraftParams struct {
	IntegrationID    string `json:"integration_id,omitempty"`
	IntegrationName  string `json:"integration_name,omitempty"`
	To               string `json:"to,omitempty"`
	Cc               string `json:"cc,omitempty"`
	Subject          string `json:"subject,omitempty"`
	Body             string `json:"body"`
	ReplyToMessageID string `json:"reply_to_message_id,omitempty"`
}

func NewGmailDraftTool(store storage.Store) *GmailDraftTool {
	return &GmailDraftTool{store: store, client: &http.Client{Timeout: 30 * time.Second}}
}

func (t *GmailDraftTool) Name() string { return "gmail_create_draft" }

func (t *GmailDraftTool) Description() string {
	return "Create a Gmail DRAFT (never sends). Requires the integration's access level to be 'Read + drafts'. To answer an email pass reply_to_message_id: recipients, subject and threading are filled in automatically."
}

func (t *GmailDraftTool) Schema() map[string]interface{} {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"integration_id":      str("Gmail integration ID (optional if only one)"),
			"integration_name":    str("Gmail integration name (optional)"),
			"to":                  str("Recipient address(es), comma separated. Optional when replying."),
			"cc":                  str("Cc address(es), comma separated"),
			"subject":             str("Subject. Optional when replying."),
			"body":                str("Plain-text body"),
			"reply_to_message_id": str("ID of the message this draft answers (keeps the thread)"),
		},
		"required": []string{"body"},
	}
}

func (t *GmailDraftTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p GmailDraftParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	fail := func(msg string) (*tools.Result, error) { return &tools.Result{Success: false, Error: msg}, nil }

	if strings.TrimSpace(p.Body) == "" {
		return fail("body is required")
	}
	integration, err := SelectGmailIntegration(t.store, p.IntegrationID, p.IntegrationName)
	if err != nil {
		return fail(err.Error())
	}
	if GmailAccessLevel(integration) != GmailAccessDraft {
		return fail(fmt.Sprintf("gmail integration %q is read-only; switch its access level to \"Read + drafts\" in Caesar and reconnect", integration.Name))
	}
	token, err := GoogleAccessToken(ctx, t.store, t.client, integration)
	if err != nil {
		return fail(err.Error())
	}

	to, subject := strings.TrimSpace(p.To), strings.TrimSpace(p.Subject)
	var threadID, inReplyTo, references string
	if id := strings.TrimSpace(p.ReplyToMessageID); id != "" {
		q := url.Values{"format": {"metadata"}}
		for _, h := range []string{"From", "Reply-To", "Subject", "Message-ID", "References"} {
			q.Add("metadataHeaders", h)
		}
		var orig gmailRawMessage
		if err := gmailGet(ctx, t.client, token, "/messages/"+url.PathEscape(id), q, &orig); err != nil {
			return fail(err.Error())
		}
		threadID = orig.ThreadID
		if to == "" {
			to = orig.Payload.header("Reply-To")
			if to == "" {
				to = orig.Payload.header("From")
			}
		}
		if subject == "" {
			subject = orig.Payload.header("Subject")
			if !strings.HasPrefix(strings.ToLower(subject), "re:") {
				subject = "Re: " + subject
			}
		}
		inReplyTo = orig.Payload.header("Message-ID")
		references = strings.TrimSpace(orig.Payload.header("References") + " " + inReplyTo)
	}
	if to == "" {
		return fail("to is required when not replying to a message")
	}
	if _, err := mail.ParseAddressList(to); err != nil {
		return fail("invalid to address: " + err.Error())
	}
	if cc := strings.TrimSpace(p.Cc); cc != "" {
		if _, err := mail.ParseAddressList(cc); err != nil {
			return fail("invalid cc address: " + err.Error())
		}
	}

	raw := buildRFC822(to, strings.TrimSpace(p.Cc), subject, p.Body, inReplyTo, references)
	msg := map[string]interface{}{"raw": base64.URLEncoding.EncodeToString(raw)}
	if threadID != "" {
		msg["threadId"] = threadID
	}
	var created struct {
		ID      string `json:"id"`
		Message struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"message"`
	}
	if err := gmailDo(ctx, t.client, token, http.MethodPost, "/drafts", nil, map[string]interface{}{"message": msg}, &created); err != nil {
		return fail(err.Error())
	}
	out, err := marshalIndented(map[string]interface{}{
		"draft_id":   created.ID,
		"message_id": created.Message.ID,
		"thread_id":  created.Message.ThreadID,
		"to":         to,
		"subject":    subject,
		"note":       "Draft saved in Gmail. It has NOT been sent.",
	})
	if err != nil {
		return fail(err.Error())
	}
	return &tools.Result{Success: true, Output: out}, nil
}

func buildRFC822(to, cc, subject, body, inReplyTo, references string) []byte {
	var b bytes.Buffer
	b.WriteString("To: " + sanitizeHeader(to) + "\r\n")
	if cc != "" {
		b.WriteString("Cc: " + sanitizeHeader(cc) + "\r\n")
	}
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", sanitizeHeader(subject)) + "\r\n")
	if inReplyTo != "" {
		b.WriteString("In-Reply-To: " + sanitizeHeader(inReplyTo) + "\r\n")
		b.WriteString("References: " + sanitizeHeader(references) + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(strings.ReplaceAll(body, "\r\n", "\n")))
	_ = w.Close()
	return b.Bytes()
}

// sanitizeHeader blocks header injection through CR/LF.
func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

var _ tools.Tool = (*GmailDraftTool)(nil)
