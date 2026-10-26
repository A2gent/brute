package integrationtools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

type certFakeCDP struct {
	mu       sync.Mutex
	methods  []string
	ignore   []bool
	failCert error
	events   chan *cdp.Event
}

func (f *certFakeCDP) Event() <-chan *cdp.Event { return f.events }

func (f *certFakeCDP) Call(ctx context.Context, _, method string, params interface{}) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods = append(f.methods, method)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "Security.setIgnoreCertificateErrors":
		if f.failCert != nil {
			return nil, f.failCert
		}
		f.ignore = append(f.ignore, params.(proto.SecuritySetIgnoreCertificateErrors).Ignore)
	case "Target.createTarget":
		return json.Marshal(map[string]string{"targetId": "new-target"})
	case "Target.attachToTarget":
		return json.Marshal(map[string]string{"sessionId": "s1"})
	}
	return []byte("{}"), nil
}

func (f *certFakeCDP) index(method string) int {
	for i, m := range f.methods {
		if m == method {
			return i
		}
	}
	return -1
}

func newCertTool(t *testing.T, f *certFakeCDP) *BrowserChromeTool {
	t.Helper()
	f.events = make(chan *cdp.Event)
	t.Cleanup(func() { close(f.events) })
	b := rod.New().Client(f)
	// Connect initializes rod's event hub; then drop its setup calls from the log.
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	f.methods = nil
	return &BrowserChromeTool{
		browser:        b,
		sessionTargets: map[string]proto.TargetTargetID{},
	}
}

func TestEnsureBrowserPageIgnoresCertErrorsBeforeCreatingPage(t *testing.T) {
	f := &certFakeCDP{}
	tool := newCertTool(t, f)
	ctx := context.Background()
	if err := tool.ensureBrowserAndPageAttemptForSession(ctx, ctx, "s"); err != nil {
		t.Fatal(err)
	}
	cert, create := f.index("Security.setIgnoreCertificateErrors"), f.index("Target.createTarget")
	if cert < 0 || create < 0 || cert > create || len(f.ignore) != 1 || !f.ignore[0] {
		t.Fatalf("want ignore=true before createTarget, got %v ignore=%v", f.methods, f.ignore)
	}
}

func TestEnsureBrowserPageIgnoresCertErrorsBeforeReusingPage(t *testing.T) {
	f := &certFakeCDP{}
	tool := newCertTool(t, f)
	tool.sessionTargets["s"] = "existing"
	ctx := context.Background()
	if err := tool.ensureBrowserAndPageAttemptForSession(ctx, ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if f.index("Target.createTarget") >= 0 {
		t.Fatalf("expected page reuse, got %v", f.methods)
	}
	cert, info := f.index("Security.setIgnoreCertificateErrors"), f.index("Target.getTargetInfo")
	if cert < 0 || info < 0 || cert > info {
		t.Fatalf("want ignore before reuse check, got %v", f.methods)
	}
}

func TestEnsureBrowserPageCertErrorsPropagate(t *testing.T) {
	boom := errors.New("cdp boom")
	f := &certFakeCDP{failCert: boom}
	tool := newCertTool(t, f)
	ctx := context.Background()
	err := tool.ensureBrowserAndPageAttemptForSession(ctx, ctx, "s")
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped cdp error, got %v", err)
	}
	if f.index("Target.createTarget") >= 0 {
		t.Fatal("page created despite cert setup failure")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	f2 := &certFakeCDP{}
	if err := newCertTool(t, f2).ensureBrowserAndPageAttemptForSession(cancelled, ctx, "s"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
