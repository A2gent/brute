package integrationtools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// newFixtureActBrowser loads testdata/browser_act_fixture.html in a throwaway headless Chrome.
// It skips rather than fails when no Chrome binary is installed, so the suite stays runnable offline.
func newFixtureActBrowser(t *testing.T) *rodActBrowser {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping browser fixture test in short mode")
	}
	binary, found := launcher.LookPath()
	if !found {
		t.Skip("no Chrome binary found; skipping the browser fixture test")
	}

	l := launcher.New().Bin(binary).Headless(true).UserDataDir(t.TempDir())
	url, err := l.Launch()
	if err != nil {
		t.Skipf("could not launch Chrome: %v", err)
	}
	t.Cleanup(l.Cleanup)

	browser := rod.New().ControlURL(url)
	if err := browser.Connect(); err != nil {
		t.Skipf("could not connect to Chrome: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		t.Fatalf("failed to open a page: %v", err)
	}
	// A fixed viewport keeps the below-the-fold assertions deterministic.
	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: 1120, Height: 780, DeviceScaleFactor: 1,
	}); err != nil {
		t.Fatalf("failed to set the viewport: %v", err)
	}
	fixture, err := filepath.Abs(filepath.Join("testdata", "browser_act_fixture.html"))
	if err != nil {
		t.Fatalf("failed to resolve the fixture path: %v", err)
	}
	if err := page.Navigate("file://" + fixture); err != nil {
		t.Fatalf("failed to open the fixture: %v", err)
	}
	if err := page.WaitLoad(); err != nil {
		t.Fatalf("fixture did not load: %v", err)
	}
	return &rodActBrowser{page: page}
}

func TestActSnapshotOffersOnlySafeVisibleControls(t *testing.T) {
	browser := newFixtureActBrowser(t)
	snap, err := browser.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}

	labels := map[string]actAction{}
	for _, action := range snap.Actions {
		labels[action.Kind+":"+action.Label] = action
	}
	if _, ok := labels["fill:Search query"]; !ok {
		t.Fatalf("expected an editable Search query field, got %v", actionLabels(snap))
	}
	if _, ok := labels["click:Search"]; !ok {
		t.Fatalf("expected a clickable Search button, got %v", actionLabels(snap))
	}
	if _, ok := labels["select:Sort by -> Newest"]; !ok {
		t.Fatalf("expected the unselected dropdown option, got %v", actionLabels(snap))
	}
	if _, ok := labels["click:Agree to terms"]; !ok {
		t.Fatalf("expected the checkbox, got %v", actionLabels(snap))
	}

	// Credential, upload, hidden, disabled and below-the-fold controls must never be offered.
	for _, forbidden := range []string{"Password", "Attach file", "Disabled action", "Below the fold link"} {
		for _, action := range snap.Actions {
			if strings.Contains(action.Label, forbidden) {
				t.Errorf("%q must not be an offered action: %+v", forbidden, action)
			}
		}
	}
	if len(snap.Marker) == 0 || len(snap.PageKey) == 0 || len(snap.Guards) == 0 {
		t.Fatalf("snapshot is missing freshness data: marker=%s pageKey=%s guards=%d",
			snap.Marker, snap.PageKey, len(snap.Guards))
	}
	if !strings.Contains(snap.Text, "Fixture search") {
		t.Fatalf("expected visible page text, got %q", snap.Text)
	}
}

func TestActSnapshotControlValues(t *testing.T) {
	browser := newFixtureActBrowser(t)
	ctx := context.Background()
	for _, checked := range []string{"false", "true", "false"} {
		snap, err := browser.Observe(ctx)
		if err != nil {
			t.Fatalf("Observe failed: %v", err)
		}
		checkbox := findActAction(t, snap, "click", "Agree to terms")
		if checkbox.Checked != checked || checkbox.Value != "" {
			t.Errorf("checkbox: checked=%q value=%q, want checked=%s and no submission value", checkbox.Checked, checkbox.Value, checked)
		}
		button := findActAction(t, snap, "click", "Search")
		if button.Value != "" {
			t.Errorf("button exposed its submission value: %q", button.Value)
		}
		field := findActAction(t, snap, "fill", "Search query")
		if field.Value != "" {
			t.Errorf("expected an empty editable field, got %q", field.Value)
		}
		rows := renderActElements(buildActSpace(snap.Actions), 20)
		if !strings.Contains(rows, "Agree to terms · checked="+checked+" (CLICK)") ||
			!strings.Contains(rows, "Search (CLICK)") ||
			!strings.Contains(rows, "Search query · empty (TYPE_TEXT,CLICK)") {
			t.Errorf("incorrect control state rendering:\n%s", rows)
		}
		// Check the raw JS shape too: empty is a real field value, not a button value.
		result, err := browser.eval(ctx, `() => {
			const actions = (`+browserActSnapshotJS+`)().actions;
			return actions.filter(a => ['checkbox', 'button'].includes(a.role)).every(a => !('value' in a)) &&
				actions.some(a => a.kind === 'fill' && a.label === 'Search query' && a.value === '');
		}`)
		if err != nil || !result.Value.Bool() {
			t.Errorf("snapshot must omit non-field values and retain empty field values: result=%v err=%v", result, err)
		}
		// Exercise the actual native checkbox click in both directions.
		if err := browser.Click(ctx, checkbox.Node); err != nil {
			t.Fatalf("checkbox Click failed: %v", err)
		}
		// A button's optional submission value must also be ignored, even when non-empty.
		if _, err := browser.eval(ctx, `() => {
			const button = document.getElementById('go');
			button.setAttribute('aria-label', 'Search');
			button.value = 'submit-search';
		}`); err != nil {
			t.Fatalf("setting button submission value failed: %v", err)
		}
	}
}

func TestActExecutionTypesAndClicksOnTheFixture(t *testing.T) {
	browser := newFixtureActBrowser(t)
	ctx := context.Background()
	snap, err := browser.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}

	if fresh, err := browser.MarkerFresh(ctx, snap); err != nil || !fresh {
		t.Fatalf("a freshly observed page must be fresh (fresh=%v err=%v)", fresh, err)
	}

	field, submit := findActAction(t, snap, "fill", "Search query"), findActAction(t, snap, "click", "Search")
	if fresh, err := browser.NodeFresh(ctx, snap, submit.Node); err != nil || !fresh {
		t.Fatalf("the submit node guard must hold before any action (fresh=%v err=%v)", fresh, err)
	}

	if err := browser.TypeText(ctx, field.Node, "widgets"); err != nil {
		t.Fatalf("TypeText failed: %v", err)
	}
	if err := browser.Click(ctx, submit.Node); err != nil {
		t.Fatalf("Click failed: %v", err)
	}

	after, err := browser.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe after acting failed: %v", err)
	}
	if !strings.Contains(after.Text, "Results for widgets") {
		t.Fatalf("expected the click handler to report the typed value, got %q", after.Text)
	}
	// The typed value must be visible as the field's current value for the next decision.
	if got := findActAction(t, after, "fill", "Search query"); got.Value != "widgets" {
		t.Fatalf("expected the field value to be widgets, got %q", got.Value)
	}
	// A page that changed must no longer satisfy the pre-action freshness check.
	if fresh, err := browser.MarkerFresh(ctx, snap); err == nil && fresh {
		t.Fatalf("the stale snapshot must not be reported as fresh after the page changed")
	}
}

func TestActTypeTextReplacesExistingContent(t *testing.T) {
	browser := newFixtureActBrowser(t)
	ctx := context.Background()
	snap, err := browser.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}
	field := findActAction(t, snap, "fill", "Search query")

	if err := browser.TypeText(ctx, field.Node, "first"); err != nil {
		t.Fatalf("first TypeText failed: %v", err)
	}
	second, err := browser.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}
	if err := browser.TypeText(ctx, findActAction(t, second, "fill", "Search query").Node, "second"); err != nil {
		t.Fatalf("second TypeText failed: %v", err)
	}

	final, err := browser.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}
	if got := findActAction(t, final, "fill", "Search query"); got.Value != "second" {
		t.Fatalf("typing must replace, not append: got %q", got.Value)
	}
}

func TestActSelectOptionMutatesTheDropdown(t *testing.T) {
	browser := newFixtureActBrowser(t)
	ctx := context.Background()
	snap, err := browser.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}
	option := findActAction(t, snap, "select", "Sort by -> Newest")

	if err := browser.SelectOption(ctx, option.Node, option.Value); err != nil {
		t.Fatalf("SelectOption failed: %v", err)
	}

	after, err := browser.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}
	// Newest is now selected, so the only remaining option is the previously selected one.
	remaining := findActAction(t, after, "select", "Sort by -> Relevance")
	if remaining.CurrentValue != "Newest" {
		t.Fatalf("expected the dropdown to report Newest as current, got %q", remaining.CurrentValue)
	}
}

func findActAction(t *testing.T, snap *actSnapshot, kind, label string) actAction {
	t.Helper()
	for _, action := range snap.Actions {
		if action.Kind == kind && action.Label == label {
			return action
		}
	}
	t.Fatalf("no %s action labelled %q in %v", kind, label, actionLabels(snap))
	return actAction{}
}

func actionLabels(snap *actSnapshot) []string {
	labels := make([]string, 0, len(snap.Actions))
	for _, action := range snap.Actions {
		labels = append(labels, action.Kind+":"+action.Label)
	}
	return labels
}
