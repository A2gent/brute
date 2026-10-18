package agent

import (
	"errors"
	"fmt"

	"github.com/A2gent/brute/internal/session"
)

// minFreeDiskBytes guards run starts: many open sessions (worktrees, builds, tool
// output) fill the disk, and a run that hits ENOSPC midway leaves half-written files.
// Pausing before the first step is cheaper to recover from.
const minFreeDiskBytes = 3 << 30

var ErrLowDiskSpace = errors.New("low disk space")

// freeDiskBytes is a variable so tests can simulate a full disk.
var freeDiskBytes = availableDiskBytes

// pauseIfLowDiskSpace pauses the session with an explanatory message when the disk
// holding its working directory has less than minFreeDiskBytes available.
func (a *Agent) pauseIfLowDiskSpace(sess *session.Session) error {
	dir := "."
	if a.toolManager != nil && a.toolManager.WorkDir() != "" {
		dir = a.toolManager.WorkDir()
	}
	free, ok := freeDiskBytes(dir)
	if !ok || free >= minFreeDiskBytes {
		return nil
	}
	err := fmt.Errorf("%w: only %.1f GB free at %s, minimum is %d GB. Free up disk space and resume the session",
		ErrLowDiskSpace, float64(free)/(1<<30), dir, minFreeDiskBytes>>30)
	sess.AddAssistantMessage("Session paused - "+err.Error()+".", nil)
	sess.SetStatus(session.StatusPaused)
	_ = a.sessionManager.Save(sess)
	return err
}
