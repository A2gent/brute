package claudecli

import (
	"errors"
	"strings"
	"testing"
)

func TestCLIErrorMessageSummarizesStdoutWithoutErrorText(t *testing.T) {
	img := strings.Repeat("A", 50000)
	stdout := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"image","source":{"data":"` + img + `"}}]}]}}`

	msg := cliErrorMessage(errors.New("signal: killed"), stdout, "")

	if len(msg) > 1000 {
		t.Fatalf("message should be compact, got %d bytes", len(msg))
	}
	for _, want := range []string{"signal: killed", `last event "user"`, "2 events"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatalf("message should not contain base64 payload: %q", msg)
	}
}

func TestCLIErrorMessagePrefersParsedResultError(t *testing.T) {
	stdout := `{"type":"result","is_error":true,"result":"You've hit your session limit"}`
	msg := cliErrorMessage(errors.New("exit status 1"), stdout, "")
	if msg != "You've hit your session limit" {
		t.Fatalf("unexpected message %q", msg)
	}
}
