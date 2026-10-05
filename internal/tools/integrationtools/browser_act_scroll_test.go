package integrationtools

import (
	"strings"
	"testing"
)

var actScrollDown = actAction{Kind: "scroll", Label: "Scroll down", Delta: 560}

func tallSnapshot(marker string, actions ...actAction) *actSnapshot {
	snap := actFixtureSnapshot(marker, append(actions, actScrollDown)...)
	snap.ScrollHeight, snap.ViewportH = 5000, 800
	return snap
}

func TestBrowserActScrollsInsteadOfGivingUpOnBlocked(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{
		tallSnapshot("top"),
		tallSnapshot("below", actSubmit),
		actFixtureSnapshot("after", actSubmit),
	}}
	client := fakeJev(t, []fakeDecision{
		{Operation: "BLOCKED"},
		{Operation: "CLICK", Target: "1"},
		{Operation: "DONE"},
	})

	output := runActTool(t, browser, client, `{"goal":"open the search result"}`)

	if !strings.Contains(output, "status: done") {
		t.Fatalf("expected done after scrolling, got:\n%s", output)
	}
	if len(browser.calls) < 2 || browser.calls[0] != "scroll:560" {
		t.Fatalf("expected a forced scroll before the click, got %v", browser.calls)
	}
}

func TestBrowserActBoundsForcedScrollsAndHintsAtMorePage(t *testing.T) {
	var snapshots []*actSnapshot
	var script []fakeDecision
	for i := 0; i <= actMaxAutoScrolls; i++ {
		snapshots = append(snapshots, tallSnapshot(string(rune('a'+i))))
		script = append(script, fakeDecision{Operation: "BLOCKED"})
	}
	browser := &fakeActBrowser{snapshots: snapshots}

	output := runActTool(t, browser, fakeJev(t, script), `{"goal":"find something absent"}`)

	if !strings.Contains(output, "status: blocked") {
		t.Fatalf("expected blocked once the scroll budget is spent, got:\n%s", output)
	}
	if len(browser.calls) != actMaxAutoScrolls {
		t.Fatalf("expected %d forced scrolls, got %v", actMaxAutoScrolls, browser.calls)
	}
	if !strings.Contains(output, "page continues") {
		t.Fatalf("expected a scroll hint for the main agent, got:\n%s", output)
	}
}
