package http

import (
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

func TestChatStreamFlusherCoalescesDeltasBeforeToolExecuting(t *testing.T) {
	var got []ChatStreamEvent
	stream := newChatStreamFlusher(func(event ChatStreamEvent) bool {
		got = append(got, event)
		return true
	}, time.Hour, 4096)
	defer stream.Close()
	for _, delta := range []string{"Hel", "lo"} {
		stream.Write(ChatStreamEvent{Type: "assistant_delta", TurnID: "turn", Delta: delta})
	}
	if len(got) != 0 {
		t.Fatal("deltas flushed before window/byte limit")
	}
	stream.Write(ChatStreamEvent{Type: "tool_executing"})
	if len(got) != 2 || got[0].Type != "assistant_delta" || got[0].Delta != "Hello" || got[1].Type != "tool_executing" {
		t.Fatalf("incorrect delta -> tool ordering: %+v", got)
	}
}

func TestChatStreamFlusherSeparatesTypeTurnStepAndProvenance(t *testing.T) {
	var got []ChatStreamEvent
	stream := newChatStreamFlusher(func(event ChatStreamEvent) bool {
		got = append(got, event)
		return true
	}, time.Hour, 4096)
	events := []ChatStreamEvent{
		{Type: "assistant_delta", TurnID: "a", Delta: "assistant"},
		{Type: "reasoning_delta", TurnID: "a", Delta: "reasoning"},
		{Type: "reasoning_delta", TurnID: "b", Delta: "new turn"},
		{Type: "reasoning_delta", TurnID: "b", Step: 2, Delta: "new step"},
		{Type: "reasoning_delta", TurnID: "b", Step: 2, Provenance: llm.NewProvenance("openai", "m1"), Delta: "new producer"},
		{Type: "reasoning_delta", TurnID: "b", Step: 2, Provenance: llm.NewProvenance("openai", "m2"), Delta: "new model"},
	}
	for _, event := range events {
		stream.Write(event)
	}
	stream.Close()
	if len(got) != len(events) {
		t.Fatalf("incompatible deltas merged: %+v", got)
	}
	for i, event := range events {
		if got[i].Delta != event.Delta || got[i].TurnID != event.TurnID || got[i].Step != event.Step {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], event)
		}
	}
}

func TestChatStreamFlusherFlushesByDeadline(t *testing.T) {
	got := make(chan ChatStreamEvent, 1)
	stream := newChatStreamFlusher(func(event ChatStreamEvent) bool {
		got <- event
		return true
	}, 20*time.Millisecond, 4096)
	defer stream.Close()
	stream.Write(ChatStreamEvent{Type: "reasoning_delta", Delta: "thinking"})
	if event := receiveSessionEvent(t, got); event.Delta != "thinking" {
		t.Fatalf("unexpected timer flush: %+v", event)
	}
}

func TestChatStreamFlusherFlushesByBytesAndOnClose(t *testing.T) {
	var got []ChatStreamEvent
	stream := newChatStreamFlusher(func(event ChatStreamEvent) bool {
		got = append(got, event)
		return true
	}, time.Hour, 4)
	stream.Write(ChatStreamEvent{Type: "assistant_delta", Delta: "ab"})
	stream.Write(ChatStreamEvent{Type: "assistant_delta", Delta: "cd"})
	if len(got) != 1 || got[0].Delta != "abcd" {
		t.Fatalf("byte threshold did not flush: %+v", got)
	}
	stream.Write(ChatStreamEvent{Type: "assistant_delta", Delta: "e"})
	stream.Close()
	stream.Close()
	if len(got) != 2 || got[1].Delta != "e" {
		t.Fatalf("close lost or duplicated pending text: %+v", got)
	}
	if stream.Write(ChatStreamEvent{Type: "done"}) || len(got) != 2 {
		t.Fatal("closed stream accepted another write")
	}
}

func TestChatStreamFlusherContinuesPublishingAfterWriteFailure(t *testing.T) {
	var got []ChatStreamEvent
	stream := newChatStreamFlusher(func(event ChatStreamEvent) bool {
		got = append(got, event)
		return false
	}, time.Hour, 4096)
	defer stream.Close()
	if stream.Write(ChatStreamEvent{Type: "status"}) {
		t.Fatal("failed status write reported success")
	}
	stream.Write(ChatStreamEvent{Type: "assistant_delta", Delta: strings.Repeat("x", 4096)})
	stream.Write(ChatStreamEvent{Type: "done"})
	if len(got) != 3 || got[2].Type != "done" {
		t.Fatalf("socket failure stopped session publication: %+v", got)
	}
}

func TestChatStreamFlusherDeadlineIsNotDelayedByContinuousTokens(t *testing.T) {
	got := make(chan ChatStreamEvent, 100)
	stream := newChatStreamFlusher(func(event ChatStreamEvent) bool {
		got <- event
		return true
	}, 20*time.Millisecond, 4096)
	defer stream.Close()
	stream.Write(ChatStreamEvent{Type: "assistant_delta", Delta: "x"})
	tokens := time.NewTicker(2 * time.Millisecond)
	defer tokens.Stop()
	deadline := time.NewTimer(500 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case <-tokens.C:
			stream.Write(ChatStreamEvent{Type: "assistant_delta", Delta: "x"})
		case event := <-got:
			if event.Type != "assistant_delta" || len(event.Delta) == 0 {
				t.Fatalf("unexpected continuous flush: %+v", event)
			}
			return
		case <-deadline.C:
			t.Fatal("continuous tokens postponed the first flush")
		}
	}
}
