package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func previewTestReply(f *previewFakeSocket, cmd previewCDPMessage) {
	b, _ := json.Marshal(previewCDPMessage{ID: cmd.ID})
	select {
	case f.messages <- b:
	case <-f.closed:
	}
}

type previewSubscribeResult struct {
	off func()
	err error
}

func previewTestSubscribe(p *browserChromePreview, ctx context.Context, id string) <-chan previewSubscribeResult {
	result := make(chan previewSubscribeResult, 1)
	go func() {
		_, off, err := p.subscribe(ctx, id)
		result <- previewSubscribeResult{off, err}
	}()
	return result
}

func previewTestWaitSubscribe(t *testing.T, result <-chan previewSubscribeResult) func() {
	t.Helper()
	select {
	case r := <-result:
		if r.err != nil {
			t.Fatal(r.err)
		}
		t.Cleanup(r.off)
		return r.off
	case <-time.After(time.Second):
		t.Fatal("subscription blocked")
		return nil
	}
}

func TestBrowserChromePreviewConcurrentFirstSubscribers(t *testing.T) {
	const subscribers = 8
	arrived := make(chan struct{}, subscribers)
	barrier := make(chan struct{})
	var release sync.Once
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-barrier:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `[{"id":"page-1","type":"page"}]`)
	}))
	defer h.Close()
	defer release.Do(func() { close(barrier) })
	f := newPreviewFakeSocket()
	defer f.Close()
	p := newBrowserChromePreview()
	p.origin = h.URL
	var mu sync.Mutex
	dials := 0
	p.dial = func(context.Context, string) (previewSocket, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		return f, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make([]<-chan previewSubscribeResult, subscribers)
	for i := range results {
		results[i] = previewTestSubscribe(p, ctx, "page-1")
	}
	for range results {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("first subscribers did not reach discovery barrier")
		}
	}
	release.Do(func() { close(barrier) })
	var offs []func()
	for _, result := range results {
		offs = append(offs, previewTestWaitSubscribe(t, result))
	}
	previewTestNoGates(t, p)
	mu.Lock()
	got := dials
	mu.Unlock()
	if got != 1 {
		t.Fatalf("concurrent first subscribers dialed %d sockets", got)
	}
	f.command(t, "Page.startScreencast")
	for _, off := range offs {
		off()
	}
	f.command(t, "Page.stopScreencast")
	select {
	case cmd := <-f.commands:
		t.Fatalf("duplicate lifecycle command: %s", cmd.Method)
	default:
	}
}

func TestBrowserChromePreviewUnsubscribeSubscribeOverlap(t *testing.T) {
	first, second := newPreviewFakeSocket(), newPreviewFakeSocket()
	first.omitReply = "Page.stopScreencast"
	defer first.Close()
	defer second.Close()
	p := previewTestManager(t, first)
	var mu sync.Mutex
	dials := 0
	p.dial = func(context.Context, string) (previewSocket, error) {
		mu.Lock()
		defer mu.Unlock()
		dials++
		if dials == 1 {
			return first, nil
		}
		return second, nil
	}
	_, off, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off)
	first.command(t, "Page.startScreencast")
	stopped := make(chan struct{})
	go func() { off(); close(stopped) }()
	stop := first.command(t, "Page.stopScreencast")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := previewTestSubscribe(p, ctx, "page-1")
	previewTestWaitGateRefs(t, p, "page-1", 2)
	select {
	case r := <-result:
		if r.off != nil {
			r.off()
		}
		t.Fatalf("subscribe completed before stop reply: %v", r.err)
	case <-time.After(30 * time.Millisecond):
	}
	previewTestReply(first, stop)
	offSecond := previewTestWaitSubscribe(t, result)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("unsubscribe blocked after stop reply")
	}
	second.command(t, "Page.startScreencast")
	offSecond()
	second.command(t, "Page.stopScreencast")
}

func TestBrowserChromePreviewTargetsIndependent(t *testing.T) {
	first, second := newPreviewFakeSocket(), newPreviewFakeSocket()
	first.omitReply = "Page.stopScreencast"
	defer first.Close()
	defer second.Close()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":"page-1","type":"page"},{"id":"page-2","type":"page"}]`)
	}))
	defer h.Close()
	p := newBrowserChromePreview()
	p.origin = h.URL
	p.dial = func(_ context.Context, u string) (previewSocket, error) {
		if strings.HasSuffix(u, "/page-1") {
			return first, nil
		}
		return second, nil
	}
	_, offFirst, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(offFirst)
	_, offSecond, err := p.subscribe(context.Background(), "page-2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(offSecond)
	first.command(t, "Page.startScreencast")
	second.command(t, "Page.startScreencast")
	stoppedFirst := make(chan struct{})
	go func() { offFirst(); close(stoppedFirst) }()
	stop := first.command(t, "Page.stopScreencast")
	// Both attaching and detaching on page-2 must ignore page-1's pending stop.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	offThird := previewTestWaitSubscribe(t, previewTestSubscribe(p, ctx, "page-2"))
	offSecond()
	stoppedSecond := make(chan struct{})
	go func() { offThird(); close(stoppedSecond) }()
	second.command(t, "Page.stopScreencast")
	select {
	case <-stoppedSecond:
	case <-time.After(time.Second):
		t.Fatal("unrelated unsubscribe blocked")
	}
	previewTestReply(first, stop)
	select {
	case <-stoppedFirst:
	case <-time.After(time.Second):
		t.Fatal("first unsubscribe blocked after reply")
	}
}

func TestBrowserChromePreviewCanceledWaiter(t *testing.T) {
	f := newPreviewFakeSocket()
	f.omitReply = "Page.startScreencast"
	defer f.Close()
	p := previewTestManager(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	first := previewTestSubscribe(p, ctx, "page-1")
	start := f.command(t, "Page.startScreencast")
	waitCtx, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	waiter := previewTestSubscribe(p, waitCtx, "page-1")
	previewTestWaitGateRefs(t, p, "page-1", 2)
	cancelWait()
	select {
	case r := <-waiter:
		if r.off != nil {
			r.off()
		}
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("waiter error = %v", r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter remained blocked behind start")
	}
	previewTestReply(f, start)
	off := previewTestWaitSubscribe(t, first)
	off()
	f.command(t, "Page.stopScreencast")
}

func previewTestWaitGateRefs(t *testing.T, p *browserChromePreview, id string, refs int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		gate := p.gates[id]
		matched := gate != nil && gate.refs == refs
		p.mu.Unlock()
		if matched {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("target %s never reached %d lifecycle references", id, refs)
		}
		time.Sleep(time.Millisecond)
	}
}

func previewTestNoGates(t *testing.T, p *browserChromePreview) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.gates) != 0 {
		t.Fatalf("leaked %d target gates", len(p.gates))
	}
}

func TestBrowserChromePreviewGateCleanup(t *testing.T) {
	p := newBrowserChromePreview()
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("page-%d", i)
		unlock, err := p.lockTarget(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if release, err := p.lockTarget(ctx, id); !errors.Is(err, context.Canceled) || release != nil {
			t.Fatalf("canceled held-gate acquisition: %v", err)
		}
		p.mu.Lock()
		refs := p.gates[id].refs
		p.mu.Unlock()
		if refs != 1 {
			t.Fatalf("cancellation leaked a waiter: %d references", refs)
		}
		unlock()
		if release, err := p.lockTarget(ctx, id); !errors.Is(err, context.Canceled) || release != nil {
			t.Fatalf("canceled idle-gate acquisition: %v", err)
		}
		previewTestNoGates(t, p)
	}
}
