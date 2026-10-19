// session_archive_progress.go keeps a live, in-memory log of what "Complete" is doing
// for a session. Completing can take a while (teardown hook, git pre-commit hooks,
// worktree removal), and the UI polls this log to show the user what is going on.
package http

import (
	"bytes"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

const archiveProgressMaxLines = 300

type archiveProgress struct {
	mu     sync.Mutex
	lines  []string
	active bool
}

var archiveProgressStore sync.Map // sessionID -> *archiveProgress

func (p *archiveProgress) add(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.lines) >= archiveProgressMaxLines {
		p.lines = p.lines[len(p.lines)-archiveProgressMaxLines+1:]
	}
	p.lines = append(p.lines, line)
}

// startArchiveProgress resets the log for a new run and returns a function that marks it finished.
func startArchiveProgress(sessionID string) func() {
	p := &archiveProgress{active: true}
	archiveProgressStore.Store(sessionID, p)
	return func() {
		p.mu.Lock()
		p.active = false
		p.mu.Unlock()
	}
}

// shareArchiveProgress makes childID log into the same buffer as rootID.
func shareArchiveProgress(rootID, childID string) {
	if v, ok := archiveProgressStore.Load(rootID); ok && rootID != childID {
		archiveProgressStore.Store(childID, v)
	}
}

func archiveProgressLog(sessionID, line string) {
	if v, ok := archiveProgressStore.Load(sessionID); ok {
		v.(*archiveProgress).add(line)
	}
}

// progressWriter splits command output into lines as they arrive, while keeping the
// whole output so callers can still build an error message from it.
type progressWriter struct {
	sessionID string
	all       bytes.Buffer
	partial   string
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.all.Write(b)
	w.partial += string(b)
	for {
		i := strings.IndexAny(w.partial, "\r\n")
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(w.partial[:i]); line != "" {
			archiveProgressLog(w.sessionID, line)
		}
		w.partial = w.partial[i+1:]
	}
	return len(b), nil
}

// runLogged runs cmd, streaming its output into the session's archive log.
// It returns the trimmed combined output.
func runLogged(sessionID string, label string, cmd *exec.Cmd) (string, error) {
	archiveProgressLog(sessionID, "$ "+label)
	w := &progressWriter{sessionID: sessionID}
	// Same writer for both streams: exec then copies them from a single goroutine.
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	if line := strings.TrimSpace(w.partial); line != "" {
		archiveProgressLog(sessionID, line)
	}
	return strings.TrimSpace(w.all.String()), err
}

// runGitLogged is runGitCommand that also streams output into the archive log.
func runGitLogged(sessionID string, dir string, args ...string) (string, error) {
	out, err := runLogged(sessionID, "git "+strings.Join(args, " "), gitCommand(dir, args...))
	if err != nil {
		if out == "" {
			out = err.Error()
		}
		return "", errors.New(out)
	}
	return out, nil
}

func (s *Server) handleArchiveProgress(w http.ResponseWriter, r *http.Request) {
	resp := struct {
		Active bool     `json:"active"`
		Lines  []string `json:"lines"`
	}{Lines: []string{}}
	if v, ok := archiveProgressStore.Load(chi.URLParam(r, "sessionID")); ok {
		p := v.(*archiveProgress)
		p.mu.Lock()
		resp.Active = p.active
		resp.Lines = append(resp.Lines, p.lines...)
		p.mu.Unlock()
	}
	s.jsonResponse(w, http.StatusOK, resp)
}
