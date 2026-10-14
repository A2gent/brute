package agent

import (
	"sync"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

const toolProgressSaveInterval = 500 * time.Millisecond

// toolProgressPersistence owns a small private session containing only the active
// assistant message. Tool callbacks never wait for DB I/O or mutate the live
// session; the loop rejoins the worker before touching the transcript again.
type toolProgressPersistence struct {
	mu      sync.Mutex
	pending []tools.ProgressEvent
	stop    chan struct{}
	done    chan struct{}
	apply   func()
}

func (a *Agent) startToolProgressPersistence(sess *session.Session, interval time.Duration) *toolProgressPersistence {
	p := &toolProgressPersistence{stop: make(chan struct{}), done: make(chan struct{})}
	index := len(sess.Messages) - 1
	message := sess.Messages[index]
	message.Metadata = cloneInterfaceMap(message.Metadata)
	scratch := &session.Session{ID: sess.ID, Messages: []session.Message{message}}
	p.apply = func() {
		if value, ok := scratch.Messages[0].Metadata[messageMetadataPendingToolResults]; ok {
			if sess.Messages[index].Metadata == nil {
				sess.Messages[index].Metadata = make(map[string]interface{})
			}
			sess.Messages[index].Metadata[messageMetadataPendingToolResults] = value
			sess.MarkMessageChanged(message.ID)
		}
	}
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		dirty := false
		flush := func(final bool) {
			p.mu.Lock()
			pending := p.pending
			p.pending = nil
			p.mu.Unlock()
			for _, progress := range pending {
				dirty = recordPendingToolProgress(scratch, progress) || dirty
			}
			value, hasProgress := scratch.Messages[0].Metadata[messageMetadataPendingToolResults]
			// A stale session Save may have overwritten a successful periodic write.
			// Reassert the latest progress at the tool boundary even without new ticks.
			if !dirty && !(final && hasProgress) {
				return
			}
			if err := a.sessionManager.SaveMessageMetadataKey(sess.ID, message.ID, messageMetadataPendingToolResults, value, time.Now()); err != nil {
				logging.Warn("Failed to persist tool progress for session %s: %v", sess.ID, err)
				return // Retry the coalesced state on the next tick or final flush.
			}
			dirty = false
		}
		for {
			select {
			case <-ticker.C:
				flush(false)
			case <-p.stop:
				flush(true)
				return
			}
		}
	}()
	return p
}

func (p *toolProgressPersistence) record(progress tools.ProgressEvent) {
	progress.Metadata = cloneInterfaceMap(progress.Metadata)
	step := metadataStepKey(progress.Metadata["parallel_step"])
	p.mu.Lock()
	// Bound the queue to one latest event per tool/parallel step. Move replacements
	// to the end so the final top-level status is the most recently received one.
	for i, previous := range p.pending {
		if previous.ToolCallID == progress.ToolCallID && metadataStepKey(previous.Metadata["parallel_step"]) == step {
			copy(p.pending[i:], p.pending[i+1:])
			p.pending = p.pending[:len(p.pending)-1]
			break
		}
	}
	p.pending = append(p.pending, progress)
	p.mu.Unlock()
}

func (p *toolProgressPersistence) finish() {
	close(p.stop)
	<-p.done
	p.apply()
}
