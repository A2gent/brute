package claudecli

import (
	"encoding/json"
	"time"

	"github.com/A2gent/brute/internal/logging"
)

// Init is a transport-readiness proxy, not a measurement of pure model latency:
// init -> text also includes network, queueing, thinking and possible tool work.
type turnTiming struct {
	transport                                     string
	reused                                        bool
	started, spawn, firstLine, init, text, result time.Time
}

func newTurnTiming(transport string, reused bool) *turnTiming {
	return &turnTiming{transport: transport, reused: reused, started: time.Now()}
}

func (t *turnTiming) observe(event cliStreamEnvelope) {
	now := time.Now()
	if t.firstLine.IsZero() {
		t.firstLine = now
	}
	if event.Type == "system" && event.Subtype == "init" && t.init.IsZero() {
		t.init = now
	}
	if event.Type == "stream_event" && event.Event.Delta.Type == "text_delta" && event.Event.Delta.Text != "" && t.text.IsZero() {
		t.text = now
	}
	if event.Type == "result" && t.result.IsZero() {
		t.result = now
	}
}

func (t *turnTiming) metadata() map[string]interface{} {
	m := map[string]interface{}{"llm_transport": t.transport, "llm_process_reused": t.reused, "llm_turn_started_at": t.started.UTC().Format(time.RFC3339Nano)}
	for key, at := range map[string]time.Time{"llm_process_spawn_at": t.spawn, "llm_first_stream_line_at": t.firstLine, "llm_system_init_at": t.init, "llm_first_text_delta_at": t.text, "llm_result_at": t.result} {
		if !at.IsZero() {
			m[key] = at.UTC().Format(time.RFC3339Nano)
		}
	}
	if !t.firstLine.IsZero() {
		m["llm_first_stream_line_ms"] = t.firstLine.Sub(t.started).Milliseconds()
	}
	if !t.init.IsZero() && !t.spawn.IsZero() {
		m["llm_spawn_to_init_ms"] = t.init.Sub(t.spawn).Milliseconds()
	}
	if !t.text.IsZero() {
		m["llm_ttft_ms"] = t.text.Sub(t.started).Milliseconds()
		if !t.init.IsZero() {
			m["llm_init_to_text_ms"] = t.text.Sub(t.init).Milliseconds()
		}
	}
	if !t.result.IsZero() {
		m["llm_turn_result_ms"] = t.result.Sub(t.started).Milliseconds()
	}
	return m
}

func (t *turnTiming) log(sessionID string) {
	data, _ := json.Marshal(t.metadata())
	logging.Info("Claude CLI turn timing session=%s %s", sessionID, data)
}
