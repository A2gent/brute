package http

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
)

// hostParentPIDEnv tells a host runtime child which process spawned it. The
// parent cleans children up on graceful shutdown, but a SIGKILL or crash skips
// that and macOS has no parent-death signal, so the child watches for itself.
const hostParentPIDEnv = "A2GENT_HOST_PARENT_PID"

const hostParentPollInterval = 2 * time.Second

// WatchHostParent calls onGone once the host runtime parent that spawned this
// process has exited (we were reparented). It is a no-op outside host runtime.
func WatchHostParent(ctx context.Context, onGone func()) {
	parentPID, err := strconv.Atoi(strings.TrimSpace(os.Getenv(hostParentPIDEnv)))
	if err != nil || parentPID <= 1 {
		return
	}
	go watchHostParent(ctx, parentPID, os.Getppid, hostParentPollInterval, onGone)
}

func watchHostParent(ctx context.Context, parentPID int, getppid func() int, interval time.Duration, onGone func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if getppid() != parentPID {
			logging.Warn("Host runtime parent (pid %d) is gone; shutting down", parentPID)
			onGone()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
