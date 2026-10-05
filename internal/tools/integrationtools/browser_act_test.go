package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm/jev"
)

// --- fake Jev server -------------------------------------------------------------------------

type fakeDecision struct {
	Operation  string
	Target     string
	Confidence float64
}

type fakeJevRequest struct {
	Questions map[string]struct {
		Type     string                     `json:"type"`
		Criteria map[string]json.RawMessage `json:"criteria"`
	} `json:"questions"`
}

// fakeJev answers the operation head from a script and fills every target head. Heads that do not
// match the chosen operation get a deliberately invalid answer, so a test fails if the loop ever
// reads a head it was not supposed to use.
func fakeJev(t *testing.T, script []fakeDecision) *jev.Client {
	t.Helper()
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request fakeJevRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("fake jev: bad request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if call >= len(script) {
			t.Errorf("fake jev: unexpected request %d, script has %d", call+1, len(script))
			http.Error(w, "script exhausted", http.StatusInternalServerError)
			return
		}
		decision := script[call]
		call++

		answers := map[string]any{}
		operationKeys := criteriaKeys(request.Questions["operation"].Criteria)
		answers["operation"] = fakeAnswer(operationKeys, decision.Operation, decision.Confidence)
		wanted := strings.ToLower(decision.Operation) + "_target"
		for name, question := range request.Questions {
			if name == "operation" {
				continue
			}
			if name == wanted {
				answers[name] = fakeAnswer(criteriaKeys(question.Criteria), decision.Target, decision.Confidence)
				continue
			}
			answers[name] = map[string]any{"type": "choice", "choice": "not-an-offered-index",
				"confidence": 2.0, "probabilities": map[string]float64{"not-an-offered-index": 7}}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"model": "fake", "answers": answers}); err != nil {
			t.Errorf("fake jev: encode: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return jev.NewClient("test-key", "fake", server.URL)
}

func criteriaKeys(criteria map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(criteria))
	for key := range criteria {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// fakeAnswer builds a distribution that passes validateActAnswer: the chosen key is argmax and the
// probabilities sum to 1.
func fakeAnswer(keys []string, choice string, confidence float64) map[string]any {
	if confidence == 0 {
		confidence = 0.95
	}
	probabilities := map[string]float64{}
	switch {
	case len(keys) == 0:
		return map[string]any{"type": "choice", "choice": choice, "confidence": confidence,
			"probabilities": map[string]float64{choice: 1}}
	case len(keys) == 1:
		probabilities[keys[0]] = 1
	default:
		rest := 0.1 / float64(len(keys)-1)
		for _, key := range keys {
			probabilities[key] = rest
		}
		probabilities[choice] = 0.9
	}
	return map[string]any{"type": "choice", "choice": choice, "confidence": confidence, "probabilities": probabilities}
}

// --- fake browser ----------------------------------------------------------------------------

type fakeActBrowser struct {
	snapshots   []*actSnapshot
	observes    int
	calls       []string
	markerFresh func(call int) bool
	nodeFresh   func(call int, node int) bool
	markerCalls int
	nodeCalls   int
}

func (b *fakeActBrowser) Observe(context.Context) (*actSnapshot, error) {
	index := min(b.observes, len(b.snapshots)-1)
	b.observes++
	return b.snapshots[index], nil
}

func (b *fakeActBrowser) MarkerFresh(context.Context, *actSnapshot) (bool, error) {
	b.markerCalls++
	if b.markerFresh != nil {
		return b.markerFresh(b.markerCalls), nil
	}
	return true, nil
}

func (b *fakeActBrowser) NodeFresh(_ context.Context, _ *actSnapshot, node int) (bool, error) {
	b.nodeCalls++
	if b.nodeFresh != nil {
		return b.nodeFresh(b.nodeCalls, node), nil
	}
	return true, nil
}

func (b *fakeActBrowser) Click(_ context.Context, node int) error {
	b.calls = append(b.calls, fmt.Sprintf("click:%d", node))
	return nil
}

func (b *fakeActBrowser) TypeText(_ context.Context, node int, text string) error {
	b.calls = append(b.calls, fmt.Sprintf("type:%d:%s", node, text))
	return nil
}

func (b *fakeActBrowser) SelectOption(_ context.Context, node int, value string) error {
	b.calls = append(b.calls, fmt.Sprintf("select:%d:%s", node, value))
	return nil
}

func (b *fakeActBrowser) Scroll(_ context.Context, delta float64) error {
	b.calls = append(b.calls, fmt.Sprintf("scroll:%.0f", delta))
	return nil
}

func (b *fakeActBrowser) Settle(context.Context, string, int) error { return nil }

func actFixtureSnapshot(marker string, actions ...actAction) *actSnapshot {
	guards := map[string]json.RawMessage{}
	for _, action := range actions {
		guards[fmt.Sprint(action.Node)] = json.RawMessage(fmt.Sprintf(`["node-%d"]`, action.Node))
	}
	return &actSnapshot{URL: "https://fixture.test/", Title: "Fixture", Text: "Fixture search",
		Actions: actions, Marker: json.RawMessage(fmt.Sprintf("%q", marker)),
		PageKey: json.RawMessage(`["pk"]`), Guards: guards}
}

var (
	actSearchField = actAction{Node: 11, Kind: "fill", Role: "textbox", Label: "Search query"}
	actSearchOpen  = actAction{Node: 11, Kind: "click", Role: "textbox", Label: "Open Search query"}
	actSubmit      = actAction{Node: 12, Kind: "click", Role: "button", Label: "Search"}
)

func runActTool(t *testing.T, browser actBrowser, client *jev.Client, params string) string {
	t.Helper()
	tool := NewBrowserActTool(nil, client)
	tool.openProxy = func(context.Context) (actBrowser, func(), error) { return browser, func() {}, nil }
	result, err := tool.Execute(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	return result.Output
}

// --- policy tests ----------------------------------------------------------------------------

func TestBuildActSpaceGivesOneIndexPerNode(t *testing.T) {
	space := buildActSpace([]actAction{
		actSearchField, actSearchOpen, actSubmit,
		{Node: 13, Kind: "select", Role: "combobox", Label: "Sort by -> Newest", Value: "new", CurrentValue: "Relevance"},
		{Node: 13, Kind: "select", Role: "combobox", Label: "Sort by -> Oldest", Value: "old", CurrentValue: "Relevance"},
		{Kind: "scroll", Label: "Scroll down", Delta: 560},
	})

	if len(space.Elements) != 3 {
		t.Fatalf("expected 3 elements, got %d: %+v", len(space.Elements), space.Elements)
	}
	// Node 11 supports both typing and clicking but must occupy a single index.
	first := space.Elements[0]
	if first.Index != "1" || first.Label != "Search query" {
		t.Fatalf("unexpected first element %+v", first)
	}
	if len(first.Operations) != 2 || !containsString(first.Operations, "TYPE_TEXT") || !containsString(first.Operations, "CLICK") {
		t.Fatalf("expected TYPE_TEXT and CLICK on element 1, got %v", first.Operations)
	}
	if got := space.Elements[2]; got.Value != "Relevance" || len(got.Options) != 2 || got.Options[1].Index != "3:2" {
		t.Fatalf("unexpected select element %+v", got)
	}
	if _, ok := space.Targets["SELECT"]["3:2"]; !ok {
		t.Fatalf("expected select target 3:2, got %v", space.Targets["SELECT"])
	}
	if _, ok := space.Controls["SCROLL_DOWN"]; !ok {
		t.Fatalf("expected a SCROLL_DOWN control, got %v", space.Controls)
	}
	// A scroll candidate has no node, so it must not become an element row.
	if _, ok := space.Targets["CLICK"]["4"]; ok {
		t.Fatalf("scroll leaked into the click targets: %v", space.Targets["CLICK"])
	}
}

func TestBuildActQuestionsOffersOnlyAvailableOperations(t *testing.T) {
	space := buildActSpace([]actAction{actSubmit})
	questions, operations := buildActQuestions(space, "do the thing")

	for _, required := range []string{"CLICK", "WAIT", "DONE", "BLOCKED"} {
		if _, ok := operations[required]; !ok {
			t.Fatalf("expected %s to be offered, got %v", required, operations)
		}
	}
	for _, absent := range []string{"TYPE_TEXT", "SELECT", "SCROLL_DOWN"} {
		if _, ok := operations[absent]; ok {
			t.Fatalf("%s must not be offered when no target exists: %v", absent, operations)
		}
	}
	if _, ok := questions["click_target"]; !ok {
		t.Fatalf("expected a click_target head, got %v", questions)
	}
	if _, ok := questions["type_text_target"]; ok {
		t.Fatalf("must not ask for a type_text target when nothing is editable")
	}
}

func TestValidateActAnswerRejectsMalformedDistributions(t *testing.T) {
	offered := map[string]string{"CLICK": "click", "DONE": "done"}
	cases := map[string]jev.Answer{
		"unoffered choice":  {Type: "choice", Choice: "WAIT", Confidence: 0.9, Probabilities: map[string]float64{"CLICK": 0.5, "DONE": 0.5}},
		"missing option":    {Type: "choice", Choice: "CLICK", Confidence: 0.9, Probabilities: map[string]float64{"CLICK": 1}},
		"does not sum":      {Type: "choice", Choice: "CLICK", Confidence: 0.9, Probabilities: map[string]float64{"CLICK": 0.6, "DONE": 0.6}},
		"choice not argmax": {Type: "choice", Choice: "CLICK", Confidence: 0.9, Probabilities: map[string]float64{"CLICK": 0.3, "DONE": 0.7}},
		"bad confidence":    {Type: "choice", Choice: "CLICK", Confidence: 1.4, Probabilities: map[string]float64{"CLICK": 0.7, "DONE": 0.3}},
		"wrong type":        {Type: "score", Choice: "CLICK", Confidence: 0.9, Probabilities: map[string]float64{"CLICK": 0.7, "DONE": 0.3}},
	}
	for name, answer := range cases {
		if err := validateActAnswer(answer, offered); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	valid := jev.Answer{Type: "choice", Choice: "CLICK", Confidence: 0.9,
		Probabilities: map[string]float64{"CLICK": 0.7, "DONE": 0.3}}
	if err := validateActAnswer(valid, offered); err != nil {
		t.Fatalf("valid answer rejected: %v", err)
	}
}

// --- loop tests ------------------------------------------------------------------------------

func TestBrowserActRunCompletesAGoal(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{
		actFixtureSnapshot("a", actSearchField, actSearchOpen, actSubmit),
		actFixtureSnapshot("b", actSearchField, actSearchOpen, actSubmit),
		actFixtureSnapshot("c", actSearchField, actSearchOpen, actSubmit),
	}}
	client := fakeJev(t, []fakeDecision{
		{Operation: "TYPE_TEXT", Target: "1"},
		{Operation: "CLICK", Target: "2"},
		{Operation: "DONE"},
	})

	output := runActTool(t, browser, client,
		`{"goal":"search for widgets","values":{"Search query":"widgets"}}`)

	if !strings.Contains(output, "status: done") {
		t.Fatalf("expected a done status, got:\n%s", output)
	}
	want := []string{"type:11:widgets", "click:12"}
	if fmt.Sprint(browser.calls) != fmt.Sprint(want) {
		t.Fatalf("expected calls %v, got %v", want, browser.calls)
	}
	if !strings.Contains(output, "TYPE_TEXT") || !strings.Contains(output, `"widgets"`) {
		t.Fatalf("trace should show the typed value, got:\n%s", output)
	}
	// Success paths must not spend tokens on the element table.
	if strings.Contains(output, "elements:") {
		t.Fatalf("element table leaked into a success result:\n%s", output)
	}
}

func TestBrowserActRunBailsBelowConfidenceFloor(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{actFixtureSnapshot("a", actSubmit)}}
	client := fakeJev(t, []fakeDecision{{Operation: "CLICK", Target: "1", Confidence: 0.4}})

	output := runActTool(t, browser, client, `{"goal":"click search"}`)

	if !strings.Contains(output, "status: low_confidence") {
		t.Fatalf("expected low_confidence, got:\n%s", output)
	}
	if len(browser.calls) != 0 {
		t.Fatalf("nothing may be executed below the floor, got %v", browser.calls)
	}
	// The bail-out is exactly when the caller needs page detail to take over.
	if !strings.Contains(output, "elements:") || !strings.Contains(output, "[1]") {
		t.Fatalf("expected the element table on bail-out, got:\n%s", output)
	}
}

func TestBrowserActRunStopsAfterRepeatedUncertainSteps(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{
		actFixtureSnapshot("a", actSubmit), actFixtureSnapshot("b", actSubmit),
		actFixtureSnapshot("c", actSubmit), actFixtureSnapshot("d", actSubmit),
		actFixtureSnapshot("e", actSubmit),
	}}
	script := make([]fakeDecision, 5)
	for i := range script {
		script[i] = fakeDecision{Operation: "CLICK", Target: "1", Confidence: 0.7}
	}
	client := fakeJev(t, script)

	output := runActTool(t, browser, client, `{"goal":"keep clicking"}`)

	if !strings.Contains(output, "status: low_confidence") {
		t.Fatalf("expected low_confidence after the uncertainty budget, got:\n%s", output)
	}
	if len(browser.calls) != actMaxUncertain {
		t.Fatalf("expected %d executed clicks before bailing, got %v", actMaxUncertain, browser.calls)
	}
	if !strings.Contains(output, "uncertain") {
		t.Fatalf("expected uncertain markers in the trace, got:\n%s", output)
	}
}

func TestBrowserActRunReturnsBlocked(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{actFixtureSnapshot("a", actSubmit)}}
	client := fakeJev(t, []fakeDecision{{Operation: "BLOCKED"}})

	output := runActTool(t, browser, client, `{"goal":"do the impossible"}`)

	if !strings.Contains(output, "status: blocked") {
		t.Fatalf("expected blocked, got:\n%s", output)
	}
	if len(browser.calls) != 0 {
		t.Fatalf("blocked must not execute anything, got %v", browser.calls)
	}
}

func TestBrowserActRunNeedsTextWithoutAValue(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{actFixtureSnapshot("a", actSearchField, actSubmit)}}
	client := fakeJev(t, []fakeDecision{{Operation: "TYPE_TEXT", Target: "1"}})

	output := runActTool(t, browser, client, `{"goal":"search for something"}`)

	if !strings.Contains(output, "status: needs_text") {
		t.Fatalf("expected needs_text, got:\n%s", output)
	}
	if len(browser.calls) != 0 {
		t.Fatalf("nothing may be typed without a value, got %v", browser.calls)
	}
	if !strings.Contains(output, "Search query") {
		t.Fatalf("expected the field label in the note, got:\n%s", output)
	}
}

func TestBrowserActRunDiscardsStaleDecisionWithoutRetrying(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{
		actFixtureSnapshot("a", actSubmit), actFixtureSnapshot("b", actSubmit),
		actFixtureSnapshot("c", actSubmit),
	}}
	// The node guard fails on the first execution attempt only.
	browser.nodeFresh = func(call int, _ int) bool { return call > 1 }
	client := fakeJev(t, []fakeDecision{
		{Operation: "CLICK", Target: "1"},
		{Operation: "CLICK", Target: "1"},
		{Operation: "DONE"},
	})

	output := runActTool(t, browser, client, `{"goal":"click search once"}`)

	if !strings.Contains(output, "status: done") {
		t.Fatalf("expected done, got:\n%s", output)
	}
	// A stale decision is re-decided, never replayed: exactly one click must reach the browser.
	if fmt.Sprint(browser.calls) != "[click:12]" {
		t.Fatalf("expected a single click, got %v", browser.calls)
	}
}

func TestBrowserActRunBlocksOnStall(t *testing.T) {
	// Every observation returns the same marker, so no action changes the page.
	stalled := actFixtureSnapshot("same", actSubmit)
	browser := &fakeActBrowser{snapshots: []*actSnapshot{stalled}}
	client := fakeJev(t, []fakeDecision{
		{Operation: "CLICK", Target: "1"},
		{Operation: "CLICK", Target: "1"},
		{Operation: "CLICK", Target: "1"},
	})

	output := runActTool(t, browser, client, `{"goal":"click a dead button"}`)

	if !strings.Contains(output, "status: blocked") {
		t.Fatalf("expected blocked on stall, got:\n%s", output)
	}
	if len(browser.calls) != actStallWindow {
		t.Fatalf("expected %d clicks before the stall guard fired, got %v", actStallWindow, browser.calls)
	}
}

func TestBrowserActRunHonoursMaxSteps(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{
		actFixtureSnapshot("a", actSubmit), actFixtureSnapshot("b", actSubmit),
		actFixtureSnapshot("c", actSubmit),
	}}
	client := fakeJev(t, []fakeDecision{
		{Operation: "CLICK", Target: "1"},
		{Operation: "CLICK", Target: "1"},
	})

	output := runActTool(t, browser, client, `{"goal":"click forever","max_steps":2}`)

	if !strings.Contains(output, "status: max_steps") {
		t.Fatalf("expected max_steps, got:\n%s", output)
	}
	if len(browser.calls) != 2 {
		t.Fatalf("expected exactly 2 actions, got %v", browser.calls)
	}
}

func TestBrowserActRunReportsClassifierFailureWithoutActing(t *testing.T) {
	browser := &fakeActBrowser{snapshots: []*actSnapshot{actFixtureSnapshot("a", actSubmit)}}
	// An empty script makes the fake return HTTP 500 on the first request.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	output := runActTool(t, browser, jev.NewClient("k", "fake", server.URL), `{"goal":"anything"}`)

	if !strings.Contains(output, "status: error") || !strings.Contains(output, "nothing executed") {
		t.Fatalf("expected an error status with nothing executed, got:\n%s", output)
	}
	if len(browser.calls) != 0 {
		t.Fatalf("a classifier failure must not act, got %v", browser.calls)
	}
}

func TestBrowserActRequiresGoalAndCredentials(t *testing.T) {
	tool := NewBrowserActTool(nil, nil)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"goal":""}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "goal is required") {
		t.Fatalf("expected a goal validation error, got %+v", result)
	}
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"goal":"go"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "Jev credentials") {
		t.Fatalf("expected a credentials error, got %+v", result)
	}
}
