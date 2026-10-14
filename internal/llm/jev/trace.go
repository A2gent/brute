package jev

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const TraceMetadataKey = "jev_requests"

type traceKey struct{}

// Trace records decisions, not state, instructions, credentials or upstream error
// bodies. Tool results already retain the source; duplicating it bloats sessions
// and can persist sensitive data in an otherwise innocuous diagnostic record.
type Trace struct {
	mu      sync.Mutex
	records []RequestTrace
}
type RequestTrace struct {
	Model      string             `json:"model"`
	Status     string             `json:"status"`
	DurationMs int64              `json:"duration_ms"`
	StateBytes int                `json:"state_bytes"`
	Questions  map[string]string  `json:"questions"`
	Response   *SystemOneResponse `json:"response,omitempty"`
}

func WithTrace(ctx context.Context) (context.Context, *Trace) {
	trace := &Trace{}
	return context.WithValue(ctx, traceKey{}, trace), trace
}
func (t *Trace) Records() []RequestTrace {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]RequestTrace(nil), t.records...)
}
func (t *Trace) Attach(metadata map[string]interface{}) map[string]interface{} {
	records := t.Records()
	if len(records) == 0 {
		return metadata
	}
	out := make(map[string]interface{}, len(metadata)+1)
	for k, v := range metadata {
		out[k] = v
	}
	// Admission may add requests after a tool's own Jev invocation.
	var previous []RequestTrace
	if raw, err := json.Marshal(out[TraceMetadataKey]); err == nil {
		_ = json.Unmarshal(raw, &previous)
	}
	out[TraceMetadataKey] = append(previous, records...)
	return out
}
func recordRequest(ctx context.Context, payload SystemOneRequest, response *SystemOneResponse, err error, started time.Time) {
	trace, _ := ctx.Value(traceKey{}).(*Trace)
	if trace == nil {
		return
	}
	record := RequestTrace{Model: payload.Model, Status: "ok", DurationMs: time.Since(started).Milliseconds(), StateBytes: len(payload.StateString()), Questions: map[string]string{}, Response: traceResponse(payload, response)}
	for key, q := range payload.Questions {
		record.Questions[key] = q.Type
	}
	if err != nil {
		record.Status = "error"
		record.Response = nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.records = append(trace.records, record)
}

// Treat upstream strings as untrusted: retain only expected questions, types
// and choice labels. A provider echo must not turn diagnostics into source storage.
func traceResponse(payload SystemOneRequest, response *SystemOneResponse) *SystemOneResponse {
	if response == nil {
		return nil
	}
	out := &SystemOneResponse{Model: payload.Model, Usage: response.Usage, Answers: map[string]Answer{}}
	for key, question := range payload.Questions {
		answer, ok := response.Answers[key]
		if !ok {
			continue
		}
		safe := Answer{Type: question.Type, Confidence: answer.Confidence}
		switch question.Type {
		case "score":
			safe.Score = answer.Score
		case "noul":
			safe.Noul = answer.Noul
		case "choice":
			// Criteria can be typed maps or structured JSON; normalize locally without
			// persisting descriptions, which may contain user-provided source text.
			raw, err := json.Marshal(question.Criteria)
			if err != nil {
				continue
			}
			var criteria map[string]json.RawMessage
			if json.Unmarshal(raw, &criteria) != nil {
				continue
			}
			if _, ok := criteria[answer.Choice]; ok {
				safe.Choice = answer.Choice
			}
			safe.Probabilities = map[string]float64{}
			for label, probability := range answer.Probabilities {
				if _, ok := criteria[label]; ok {
					safe.Probabilities[label] = probability
				}
			}
		}
		out.Answers[key] = safe
	}
	return out
}
