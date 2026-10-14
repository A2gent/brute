package tui

import (
	"errors"
	"testing"
)

func TestHandleSelfUpdateDone(t *testing.T) {
	RestartRequested = false
	m, cmd := Model{}.handleSelfUpdateDone(selfUpdateDoneMsg{err: errors.New("boom")})
	if RestartRequested || cmd != nil {
		t.Fatal("failed update must not restart")
	}
	if got := m.(Model).messages; len(got) != 1 || got[0].role != "error" {
		t.Fatalf("expected one error message, got %+v", got)
	}

	_, cmd = Model{}.handleSelfUpdateDone(selfUpdateDoneMsg{})
	if !RestartRequested || cmd == nil {
		t.Fatal("successful update must request restart and quit")
	}
	RestartRequested = false
}
