package http

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchHostParentFiresWhenReparented(t *testing.T) {
	var ppid atomic.Int64
	ppid.Store(4242)
	gone := make(chan struct{})
	go watchHostParent(t.Context(), 4242, func() int { return int(ppid.Load()) }, time.Millisecond, func() { close(gone) })

	select {
	case <-gone:
		t.Fatal("must not fire while the parent is alive")
	case <-time.After(20 * time.Millisecond):
	}
	ppid.Store(1) // parent died, launchd/init adopted us
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatal("expected onGone after reparenting")
	}
}

func TestWatchHostParentStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		watchHostParent(ctx, 4242, func() int { return 4242 }, time.Millisecond, func() { t.Error("unexpected onGone") })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher must return when ctx is cancelled")
	}
}

func TestWatchHostParentIgnoresMissingEnv(t *testing.T) {
	t.Setenv(hostParentPIDEnv, "")
	WatchHostParent(t.Context(), func() { t.Error("must not fire without a host parent") })
	time.Sleep(10 * time.Millisecond)
}
