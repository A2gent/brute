package integrationtools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramSendMessageSendsHTMLAndReportsTelegramErrors(t *testing.T) {
	var bodies []map[string]interface{}
	tool := &TelegramSendMessageTool{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(raw, &payload)
		bodies = append(bodies, payload)
		reply := `{"ok":true}`
		if payload["parse_mode"] == "HTML" {
			reply = `{"ok":false,"description":"Bad Request: can't parse entities: Unsupported start tag"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply)), Header: http.Header{}}, nil
	})}}

	errMsg, err := tool.send(context.Background(), "token", "42", 0, "<b>hi</b>", "HTML")
	if err != nil || !strings.Contains(errMsg, "can't parse entities") {
		t.Fatalf("expected Telegram's parse error to be returned, got %q / %v", errMsg, err)
	}
	if bodies[0]["parse_mode"] != "HTML" || bodies[0]["text"] != "<b>hi</b>" {
		t.Fatalf("unexpected payload: %#v", bodies[0])
	}

	errMsg, err = tool.send(context.Background(), "token", "42", 7, "plain", "")
	if err != nil || errMsg != "" {
		t.Fatalf("plain resend should succeed, got %q / %v", errMsg, err)
	}
	if _, has := bodies[1]["parse_mode"]; has || bodies[1]["message_thread_id"] != float64(7) {
		t.Fatalf("unexpected plain payload: %#v", bodies[1])
	}
}
