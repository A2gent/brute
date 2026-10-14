package http

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func receiveSessionEvent(t *testing.T, events <-chan ChatStreamEvent) ChatStreamEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("subscription closed before all events were delivered")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lossless session event delivery")
		return ChatStreamEvent{}
	}
}

func TestSessionEventsSlowSubscriberPreservesTextAndNonDeltas(t *testing.T) {
	server := &Server{}
	events, unsubscribe := server.SubscribeSessionEvents("slow")
	defer unsubscribe()

	var wantText strings.Builder
	var wantTypes []string
	// Deliberately publish without reading, exceeding the old 128-event channel.
	for step := 0; step < 200; step++ {
		for token := 0; token < 10; token++ {
			delta := fmt.Sprintf("%d:%d ", step, token)
			wantText.WriteString(delta)
			server.publishSessionEvent("slow", ChatStreamEvent{Type: "assistant_delta", TurnID: "turn", Delta: delta})
		}
		for _, kind := range []string{"tool_executing", "tool_progress", "tool_completed"} {
			server.publishSessionEvent("slow", ChatStreamEvent{Type: kind, Step: step})
			wantTypes = append(wantTypes, fmt.Sprintf("%s:%d", kind, step))
		}
	}
	server.publishSessionEvent("slow", ChatStreamEvent{Type: "done"})
	wantTypes = append(wantTypes, "done:0")

	var text strings.Builder
	var types []string
	for {
		event := receiveSessionEvent(t, events)
		if event.Type == "assistant_delta" {
			text.WriteString(event.Delta)
		} else {
			types = append(types, fmt.Sprintf("%s:%d", event.Type, event.Step))
			if event.Type == "tool_executing" {
				prefix := wantText.String()
				if !strings.HasPrefix(prefix, text.String()) || !strings.HasSuffix(text.String(), fmt.Sprintf("%d:9 ", event.Step)) {
					t.Fatalf("tool_executing step %d arrived before its text: %q", event.Step, text.String())
				}
			}
		}
		if event.Type == "done" {
			break
		}
	}
	if text.String() != wantText.String() {
		t.Fatalf("text truncated: got %d bytes, want %d", text.Len(), wantText.Len())
	}
	if strings.Join(types, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("non-delta events lost or reordered: got %v, want %v", types, wantTypes)
	}
}

func TestSessionEventsReplayExceedsChannelCapacity(t *testing.T) {
	server := &Server{}
	for i := 0; i < 300; i++ {
		server.publishSessionEvent("replay", ChatStreamEvent{Type: "tool_updated", TurnID: "turn", Step: i})
	}
	events, unsubscribe := server.SubscribeSessionEvents("replay")
	defer unsubscribe()
	for i := 0; i < 300; i++ {
		if event := receiveSessionEvent(t, events); event.Type != "tool_updated" || event.Step != i {
			t.Fatalf("replay event %d = %+v", i, event)
		}
	}
}

func TestSessionEventsReplaySnapshotDoesNotChangeWhenTextGrows(t *testing.T) {
	server := &Server{}
	server.publishSessionEvent("replay", ChatStreamEvent{Type: "reasoning_delta", TurnID: "turn", Delta: "first"})
	events, unsubscribe := server.SubscribeSessionEvents("replay")
	first := receiveSessionEvent(t, events)
	unsubscribe()
	for i := 0; i < 10000; i++ {
		server.publishSessionEvent("replay", ChatStreamEvent{Type: "reasoning_delta", TurnID: "turn", Delta: "x"})
	}
	if first.Delta != "first" {
		t.Fatalf("already delivered snapshot changed: %q", first.Delta)
	}
	events, unsubscribe = server.SubscribeSessionEvents("replay")
	defer unsubscribe()
	if event := receiveSessionEvent(t, events); event.Delta != "first"+strings.Repeat("x", 10000) {
		t.Fatalf("replay text corrupted: length %d", len(event.Delta))
	}
}

func TestSessionEventsUnsubscribeWhileDeliveryBlocked(t *testing.T) {
	server := &Server{}
	events, unsubscribe := server.SubscribeSessionEvents("blocked")
	for i := 0; i < 300; i++ {
		server.publishSessionEvent("blocked", ChatStreamEvent{Type: "tool_progress"})
	}
	unsubscribe()
	unsubscribe()
	for range events {
	}
	server.publishSessionEvent("blocked", ChatStreamEvent{Type: "done"})
}

func TestSessionEventsReplayDoesNotSilentlyTruncateLongTurn(t *testing.T) {
	server := &Server{}
	const count = 2200
	for i := 0; i < count; i++ {
		server.publishSessionEvent("long-turn", ChatStreamEvent{Type: "tool_updated", TurnID: "turn", Step: i})
	}
	events, unsubscribe := server.SubscribeSessionEvents("long-turn")
	defer unsubscribe()
	for i := 0; i < count; i++ {
		if event := receiveSessionEvent(t, events); event.Step != i {
			t.Fatalf("replay event %d = %+v", i, event)
		}
	}
}

func TestSessionEventsSlowSubscriberDoesNotBlockFastSubscriber(t *testing.T) {
	server := &Server{}
	_, stopSlow := server.SubscribeSessionEvents("shared")
	defer stopSlow()
	fast, stopFast := server.SubscribeSessionEvents("shared")
	defer stopFast()
	for i := 0; i < 300; i++ {
		server.publishSessionEvent("shared", ChatStreamEvent{Type: "tool_progress", Step: i})
		if event := receiveSessionEvent(t, fast); event.Step != i {
			t.Fatalf("fast subscriber event %d = %+v", i, event)
		}
	}
}

func TestSessionEventsPendingTailCompactsOnlyCompatibleDeltas(t *testing.T) {
	sub := &sessionEventSubscription{wake: make(chan struct{}, 1)}
	for i := 0; i < 10000; i++ {
		sub.enqueue(ChatStreamEvent{Type: "assistant_delta", TurnID: "a", Delta: "x"})
	}
	sub.enqueue(ChatStreamEvent{Type: "tool_executing"})
	sub.enqueue(ChatStreamEvent{Type: "assistant_delta", TurnID: "a", Delta: "after tool"})
	sub.enqueue(ChatStreamEvent{Type: "reasoning_delta", TurnID: "a", Delta: "reasoning"})
	sub.enqueue(ChatStreamEvent{Type: "reasoning_delta", TurnID: "b", Delta: "next turn"})
	if len(sub.pending) != 5 || sub.pending[0].snapshot().Delta != strings.Repeat("x", 10000) {
		t.Fatalf("incorrect pending tail compaction: %d entries", len(sub.pending))
	}
}
