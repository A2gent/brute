package agent

import (
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

// Record failed attempts as well as successful calls: otherwise the slowest
// auxiliary requests disappear from session-level latency measurements.
func recordAuxiliaryCall(sess *session.Session, operation string, started time.Time, outcome string) {
	RecordAuxiliaryCall(sess, operation, started, outcome)
}

// RecordAuxiliaryCall also records pre-run decisions made by the HTTP server.
func RecordAuxiliaryCall(sess *session.Session, operation string, started time.Time, outcome string) {
	duration := time.Since(started).Milliseconds()
	metadataSetFloat(sess, operation+"_attempt_count", metadataFloat(sess.Metadata, operation+"_attempt_count")+1)
	metadataSetFloat(sess, operation+"_total_duration_ms", metadataFloat(sess.Metadata, operation+"_total_duration_ms")+float64(duration))
	metadataSetFloat(sess, operation+"_last_duration_ms", float64(duration))
	metadataSetString(sess, operation+"_last_started_at", started.UTC().Format(time.RFC3339Nano))
	metadataSetString(sess, operation+"_last_outcome", outcome)
	logging.Info("Auxiliary LLM call: session=%s operation=%s duration_ms=%d outcome=%s", sess.ID, operation, duration, outcome)
}
