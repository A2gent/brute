package openaicodex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const cacheDebugSessions = 128

type cacheFingerprint struct {
	Tools        string   `json:"tools_hash"`
	Instructions string   `json:"instructions_hash"`
	Items        []string `json:"item_hashes"`
}

type cacheDebugState struct {
	fingerprint cacheFingerprint
	sequence    uint64
}

// Keep only hashes, bounded process-wide so a session resumed with a new client
// can still be compared. No prompt, image, tool arguments or encrypted reasoning
// content is retained by diagnostics.
var responsesCacheDebug cacheDebugTracker

type cacheDebugTracker struct {
	mu       sync.Mutex
	sequence uint64
	sessions map[string]cacheDebugState
}

type cacheObservation struct {
	cacheFingerprint
	Session      string `json:"session"`
	Model        string `json:"model"`
	Request      uint64 `json:"request"`
	Previous     uint64 `json:"previous_request"`
	FirstDiff    int    `json:"first_different_item"`
	PrefixChange string `json:"prefix_change"`
	Continuation bool   `json:"continuation"`
}

func cacheHash(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (d *cacheDebugTracker) begin(sessionID string, body []byte) *cacheObservation {
	if os.Getenv("AAGENT_LLM_CACHE_DEBUG") != "1" {
		return nil
	}
	// Hash the serialized wire fields, not the source structs: this also catches
	// schema serialization changes. Work is entirely opt-in.
	var wire struct {
		Model              string            `json:"model"`
		Tools              json.RawMessage   `json:"tools"`
		Instructions       json.RawMessage   `json:"instructions"`
		Input              []json.RawMessage `json:"input"`
		PreviousResponseID string            `json:"previous_response_id"`
	}
	if json.Unmarshal(body, &wire) != nil {
		return nil
	}
	current := cacheFingerprint{Tools: cacheHash(wire.Tools), Instructions: cacheHash(wire.Instructions), Items: make([]string, len(wire.Input))}
	for i, raw := range wire.Input {
		current.Items[i] = cacheHash(raw)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sequence++
	observation := &cacheObservation{cacheFingerprint: current, Session: sessionID, Model: wire.Model, Request: d.sequence, FirstDiff: -1, PrefixChange: "cold", Continuation: wire.PreviousResponseID != ""}
	if previous, ok := d.sessions[sessionID]; ok && sessionID != "" {
		observation.Previous = previous.sequence
		observation.PrefixChange = "none"
		limit := min(len(previous.fingerprint.Items), len(current.Items))
		for i := 0; i < limit; i++ {
			if previous.fingerprint.Items[i] != current.Items[i] {
				observation.FirstDiff = i
				break
			}
		}
		if observation.FirstDiff == -1 && len(previous.fingerprint.Items) != len(current.Items) {
			observation.FirstDiff = limit
		}
		switch {
		case previous.fingerprint.Tools != current.Tools:
			observation.PrefixChange = "tools"
		case previous.fingerprint.Instructions != current.Instructions:
			observation.PrefixChange = "instructions"
		case observation.FirstDiff != -1:
			observation.PrefixChange = "input"
		}
	}
	if sessionID != "" {
		if d.sessions == nil {
			d.sessions = make(map[string]cacheDebugState)
		}
		if _, exists := d.sessions[sessionID]; !exists && len(d.sessions) >= cacheDebugSessions {
			var oldest string
			var sequence uint64 = ^uint64(0)
			for id, state := range d.sessions {
				if state.sequence < sequence {
					oldest, sequence = id, state.sequence
				}
			}
			delete(d.sessions, oldest)
		}
		d.sessions[sessionID] = cacheDebugState{fingerprint: current, sequence: d.sequence}
	}
	raw, _ := json.Marshal(observation)
	logging.Debug("LLM_CACHE_REQUEST %s", raw)
	return observation
}

func (o *cacheObservation) finish(response *llm.ChatResponse, err error) {
	if o == nil {
		return
	}
	var usage llm.TokenUsage
	if response != nil {
		usage = response.Usage
	}
	logging.Debug("LLM_CACHE_USAGE session=%q request=%d input=%d cached=%d error=%t", o.Session, o.Request, usage.InputTokens, usage.CachedInputTokens, err != nil)
}
