package integrationtools

// Opt-in real-Jev probe. Ordinary tests never read credentials, launch Chrome, or call Jev.
//
// BROWSER_ACT_PROBE=1 JEV_API_KEY=... BROWSER_ACT_PROBE_OUTPUT=/tmp/act-probe.json \
//   go test ./internal/tools/integrationtools -run '^TestBrowserActRealJevProbe$' -count=1 -timeout=60m -v
// Optional: BROWSER_ACT_PROBE_RUNS (default 3), BROWSER_ACT_PROBE_MODE
// (both|production|coverage), BROWSER_ACT_PROBE_CHROME (binary path), JEV_MODEL,
// JEV_BASE_URL. A fixed viewport, throwaway profile and fresh page isolate trials.
//
// The concrete *jev.Client prevents a classifier decorator. A loopback forwarder records
// the exact classifier request/response BEFORE decideAct validation and run's gate, including
// rejected, malformed, terminal and failed requests. It uses a real jev.Client upstream.
// No global transport or production seam is changed. Coverage mode records decisions without
// executing model choices, then advances ONLY these trusted fixtures using their manual oracle.
// Its final success is oracle-assisted coverage, never autonomous/model success.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type probeFixture struct {
	ID     string            `json:"id"`
	Goal   string            `json:"goal"`
	Values map[string]string `json:"values,omitempty"`
}

var probeFixtures = []probeFixture{
	{ID: "search", Goal: "Search the library catalogue for widgets. Submit the search and show results for widgets.", Values: map[string]string{"Search query": "widgets"}},
	{ID: "filters", Goal: "Apply In stock only and Free shipping filters. Keep On sale off and keep the already checked Free shipping on."},
	{ID: "sort", Goal: "Sort the public articles by Newest."},
	{ID: "calendar", Goal: "Choose October 15, 2026 as the appointment date, then confirm the date."},
	{ID: "menu", Goal: "Change the reading language to French using the language menu."},
	{ID: "autocomplete", Goal: "Choose Paris, France (not Paris, Texas) as the destination using the autocomplete suggestion, then confirm the destination.", Values: map[string]string{"Destination": "Paris"}},
	{ID: "form", Goal: "Replace the previous visitor name with Ada, enter ada@example.test as the email address, and send the message.", Values: map[string]string{"Your name": "Ada", "Email address": "ada@example.test"}},
	{ID: "tabs", Goal: "Open the Details tab and expand the detailed report, not the summary."},
	{ID: "scroll", Goal: "Finish the guide using the Finish guide button at the bottom of the page. Do not restart the guide."},
	{ID: "loading", Goal: "Find the report, wait for results if they are loading, then open the Annual report."},
}

type probeChoice struct {
	Operation string `json:"operation"`
	Label     string `json:"label"`
}

type probeTruth struct {
	State    string        `json:"state"`
	Expected []probeChoice `json:"expected"`
	Success  bool          `json:"success"`
	Evidence string        `json:"evidence"`
}

type probeDecision struct {
	Sequence             int                    `json:"sequence"`
	Truth                probeTruth             `json:"manual_truth"`
	TruthError           string                 `json:"truth_error,omitempty"`
	Request              jev.SystemOneRequest   `json:"request"`
	Response             *jev.SystemOneResponse `json:"response,omitempty"`
	Operation            string                 `json:"operation"`
	Target               string                 `json:"target"`
	TargetLabel          string                 `json:"target_label"`
	OperationConfidence  *float64               `json:"operation_confidence"`
	TargetConfidence     *float64               `json:"target_confidence"`
	OperationProbability *float64               `json:"operation_probability"`
	TargetProbability    *float64               `json:"target_probability"`
	MinimumConfidence    *float64               `json:"minimum_confidence"`
	Band                 string                 `json:"production_confidence_band"`
	Valid                bool                   `json:"valid"`
	ValidationError      string                 `json:"validation_error,omitempty"`
	ClassifierError      string                 `json:"classifier_error,omitempty"`
	OperationCorrect     *bool                  `json:"operation_correct"`
	TargetCorrect        *bool                  `json:"target_correct"`
	DecisionCorrect      *bool                  `json:"decision_correct"`
	Disposition          string                 `json:"disposition"`
	ExecutionError       string                 `json:"execution_error,omitempty"`
	OracleAdvanced       bool                   `json:"oracle_advanced"`
	DurationMS           int64                  `json:"duration_ms"`
}

type probeTrial struct {
	Fixture           probeFixture    `json:"fixture"`
	Repeat            int             `json:"repeat"`
	Mode              string          `json:"mode"`
	Status            string          `json:"status"`
	Note              string          `json:"note"`
	Error             string          `json:"error,omitempty"`
	FinalSuccess      bool            `json:"final_success"`
	AutonomousSuccess *bool           `json:"autonomous_success"`
	FinalTruth        probeTruth      `json:"final_truth"`
	ExecutedSteps     []actStep       `json:"production_executed_steps,omitempty"`
	Decisions         []probeDecision `json:"decisions"`
}

type probeReport struct {
	SchemaVersion int               `json:"schema_version"`
	StartedAt     time.Time         `json:"started_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Complete      bool              `json:"complete"`
	Model         string            `json:"configured_model"`
	ChromeVersion string            `json:"chrome_version"`
	Viewport      string            `json:"viewport"`
	FixtureSHA256 map[string]string `json:"fixture_sha256"`
	Repeats       int               `json:"repeats"`
	Modes         []string          `json:"modes"`
	Medium        float64           `json:"production_medium_threshold"`
	High          float64           `json:"production_high_threshold"`
	MaxUncertain  int               `json:"production_max_uncertain"`
	MaxSteps      int               `json:"max_steps_per_trial"`
	TimeoutMS     int64             `json:"timeout_ms_per_trial"`
	Semantics     string            `json:"semantics"`
	Trials        []*probeTrial     `json:"trials"`
}

// Atomic checkpoints retain all completed decisions if a later trial fails or go test times out.
// Files contain public synthetic fixture values and model answers, never auth headers or keys.
func probeWriteReport(path string, report *probeReport) error {
	report.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".browser-act-probe-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func probeReadTruth(ctx context.Context, browser *rodActBrowser) (probeTruth, error) {
	result, err := browser.eval(ctx, "() => window.probeTruth()")
	if err != nil {
		return probeTruth{}, err
	}
	raw, err := result.Value.MarshalJSON()
	if err != nil {
		return probeTruth{}, err
	}
	var truth probeTruth
	err = json.Unmarshal(raw, &truth)
	return truth, err
}

func probeFloat(f float64) *float64 { return &f }
func probeBool(b bool) *bool        { return &b }

// Grade semantic operation/target labels against hand-authored allowed choices, irrespective of
// confidence. Wrong operation gets a false combined label; a non-target operation has target=null.
func probeGrade(d *probeDecision) {
	if d.TruthError != "" || len(d.Truth.Expected) == 0 || d.Operation == "" {
		return
	}
	op, target, combined := false, false, false
	for _, expected := range d.Truth.Expected {
		if expected.Operation == d.Operation {
			op = true
			if expected.Label == d.TargetLabel {
				target, combined = true, true
			}
		}
	}
	d.OperationCorrect, d.DecisionCorrect = probeBool(op), probeBool(combined)
	if d.Target != "" {
		d.TargetCorrect = probeBool(op && target)
	}
}

// Decode exactly the matching head, using production's distribution validator. Raw answers for
// ALL heads remain in Response even when malformed or unused; null means absent/not applicable.
func probeDecode(d *probeDecision) {
	if d.Response == nil {
		return
	}
	answer, ok := d.Response.Answers["operation"]
	if !ok {
		d.ValidationError = "missing operation answer"
		return
	}
	d.Operation = strings.TrimSpace(answer.Choice)
	d.OperationConfidence = probeFloat(answer.Confidence)
	if p, ok := answer.Probabilities[d.Operation]; ok {
		d.OperationProbability = probeFloat(p)
	}
	criteria := func(name string) map[string]json.RawMessage {
		q, ok := d.Request.Questions[name]
		if !ok {
			return nil
		}
		raw, _ := json.Marshal(q.Criteria)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		return m
	}
	offered := func(m map[string]json.RawMessage) map[string]string {
		out := map[string]string{}
		for k := range m {
			out[k] = k
		}
		return out
	}
	validation := validateActAnswer(answer, offered(criteria("operation")))
	name := strings.ToLower(d.Operation) + "_target"
	if targets := criteria(name); len(targets) > 0 {
		if target, ok := d.Response.Answers[name]; ok {
			d.Target = strings.TrimSpace(target.Choice)
			d.TargetConfidence = probeFloat(target.Confidence)
			if p, ok := target.Probabilities[d.Target]; ok {
				d.TargetProbability = probeFloat(p)
			}
			var criterion struct {
				Element string `json:"element"`
			}
			_ = json.Unmarshal(targets[d.Target], &criterion)
			d.TargetLabel = strings.TrimPrefix(criterion.Element, "["+d.Target+"] ")
			validation = errors.Join(validation, validateActAnswer(target, offered(targets)))
		} else {
			validation = errors.Join(validation, fmt.Errorf("missing %s answer", name))
		}
	}
	if validation != nil {
		d.ValidationError = validation.Error()
	} else {
		d.Valid = true
	}
	if d.Valid {
		confidence := answer.Confidence
		if d.TargetConfidence != nil {
			confidence = min(confidence, *d.TargetConfidence)
		}
		d.MinimumConfidence = probeFloat(confidence)
		switch {
		case confidence < actMediumConfidence:
			d.Band = "below_medium"
		case confidence < actHighConfidence:
			d.Band = "medium"
		default:
			d.Band = "high"
		}
	}
	probeGrade(d)
}

// Forwarder handlers and the production loop access records from different goroutines. Protect
// the complete checkpoint as well as individual appends; do not leak the upstream key into JSON.
type probeRecorder struct {
	mu         sync.Mutex
	trial      *probeTrial
	truth      func(context.Context) (probeTruth, error)
	checkpoint func() error
	writeError error
	secret     string
}

func (r *probeRecorder) scrub(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if r.secret != "" {
		text = strings.ReplaceAll(text, r.secret, "[redacted]")
	}
	return text
}

func (r *probeRecorder) saveLocked() {
	if r.checkpoint != nil {
		if err := r.checkpoint(); err != nil {
			r.writeError = err
		}
	}
}

func (r *probeRecorder) handler(upstream *jev.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/systemone" {
			http.Error(w, "only /systemone is supported", http.StatusNotFound)
			return
		}
		start := time.Now()
		d := probeDecision{Disposition: "not_executed"}
		if err := json.NewDecoder(req.Body).Decode(&d.Request); err != nil {
			d.ClassifierError = r.scrub(err)
		} else {
			if r.truth != nil {
				var err error
				d.Truth, err = r.truth(req.Context())
				d.TruthError = r.scrub(err)
			}
		}
		// Persist an in-flight row too; hung/cancelled HTTP requests must not disappear.
		r.mu.Lock()
		d.Sequence = len(r.trial.Decisions) + 1
		d.Disposition = "in_flight"
		r.trial.Decisions = append(r.trial.Decisions, d)
		index := len(r.trial.Decisions) - 1
		r.saveLocked()
		r.mu.Unlock()
		if d.ClassifierError == "" {
			var err error
			d.Response, err = upstream.SystemOne(req.Context(), d.Request)
			d.ClassifierError = r.scrub(err)
		}
		d.DurationMS = time.Since(start).Milliseconds()
		d.Disposition = "not_executed"
		probeDecode(&d)
		r.mu.Lock()
		r.trial.Decisions[index] = d
		r.saveLocked()
		r.mu.Unlock()
		if d.ClassifierError != "" {
			http.Error(w, d.ClassifierError, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.Response)
	})
}

func (r *probeRecorder) latest(update func(*probeDecision)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.trial.Decisions); n > 0 {
		update(&r.trial.Decisions[n-1])
		r.saveLocked()
	}
}

// Thin browser decorator records attempted execution and freshness rejection, not just run.Steps.
type probeBrowser struct {
	actBrowser
	recorder *probeRecorder
}

func (b *probeBrowser) outcome(err error) {
	b.recorder.latest(func(d *probeDecision) {
		d.Disposition = "executed"
		if err != nil {
			d.Disposition = "execution_error"
			if errors.Is(err, errActStale) {
				d.Disposition = "stale_rejected"
			}
			d.ExecutionError = b.recorder.scrub(err)
		}
	})
}
func (b *probeBrowser) Click(ctx context.Context, node int) error {
	err := b.actBrowser.Click(ctx, node)
	b.outcome(err)
	return err
}
func (b *probeBrowser) TypeText(ctx context.Context, node int, text string) error {
	err := b.actBrowser.TypeText(ctx, node, text)
	b.outcome(err)
	return err
}
func (b *probeBrowser) SelectOption(ctx context.Context, node int, value string) error {
	err := b.actBrowser.SelectOption(ctx, node, value)
	b.outcome(err)
	return err
}
func (b *probeBrowser) Scroll(ctx context.Context, delta float64) error {
	err := b.actBrowser.Scroll(ctx, delta)
	b.outcome(err)
	return err
}
func (b *probeBrowser) Settle(ctx context.Context, kind string, node int) error {
	b.recorder.latest(func(d *probeDecision) {
		if d.Operation == "WAIT" {
			d.Disposition = "executed"
		}
	})
	return b.actBrowser.Settle(ctx, kind, node)
}
func (b *probeBrowser) MarkerFresh(ctx context.Context, snap *actSnapshot) (bool, error) {
	fresh, err := b.actBrowser.MarkerFresh(ctx, snap)
	if !fresh && err == nil {
		b.recorder.latest(func(d *probeDecision) {
			if d.Disposition == "not_executed" {
				d.Disposition = "stale_rejected"
			}
		})
	}
	return fresh, err
}
func (b *probeBrowser) NodeFresh(ctx context.Context, snap *actSnapshot, node int) (bool, error) {
	fresh, err := b.actBrowser.NodeFresh(ctx, snap, node)
	if !fresh && err == nil {
		b.recorder.latest(func(d *probeDecision) { d.Disposition = "stale_rejected" })
	}
	return fresh, err
}

// Oracle recovery never executes a classifier pick. Re-observe after a decision: an async fixture
// may have legitimately advanced meanwhile. Execute only an offered manual target via real rod.
func probeAdvance(ctx context.Context, tool *BrowserActTool, browser *rodActBrowser, params actParams) error {
	truth, err := probeReadTruth(ctx, browser)
	if err != nil {
		return err
	}
	if truth.Success {
		return nil
	}
	if len(truth.Expected) == 0 {
		return errors.New("oracle has no expected decision")
	}
	snap, err := browser.Observe(ctx)
	if err != nil {
		return err
	}
	space := buildActSpace(snap.Actions)
	choice := truth.Expected[0]
	decision := &actDecision{Operation: choice.Operation}
	if choice.Operation == "DONE" || choice.Operation == "BLOCKED" {
		return fmt.Errorf("unexpected oracle stop before success: %s", choice.Operation)
	}
	if choice.Operation == "SCROLL_DOWN" || choice.Operation == "SCROLL_UP" {
		decision.Action, decision.HasAction = space.Controls[choice.Operation]
	}
	for target, action := range space.Targets[choice.Operation] {
		if action.Label == choice.Label {
			decision.Target, decision.Action, decision.HasAction = target, action, true
			break
		}
	}
	if choice.Operation != "WAIT" && !decision.HasAction {
		return fmt.Errorf("oracle target %s %q is not offered", choice.Operation, choice.Label)
	}
	text := ""
	if choice.Operation == "TYPE_TEXT" {
		text, err = tool.fieldText(ctx, params, decision.Action, snap)
		if err != nil {
			return err
		}
	}
	if err := tool.execute(ctx, browser, snap, decision, text); err != nil {
		return err
	}
	if err := browser.Settle(ctx, decision.Action.Kind, decision.Action.Node); err != nil {
		return err
	}
	_, err = browser.page.Context(ctx).Evaluate(rod.Eval("async () => { if (window.probeRecover) await window.probeRecover(); }").ByPromise())
	return err
}

func probeCoverage(ctx context.Context, tool *BrowserActTool, browser *rodActBrowser, recorder *probeRecorder, params actParams) (string, string) {
	history := []actHistoryEntry{}
	for step := 0; step < params.MaxSteps; step++ {
		snap, err := browser.Observe(ctx)
		if err != nil {
			return "error", recorder.scrub(err)
		}
		truth, err := probeReadTruth(ctx, browser)
		if err != nil {
			return "error", recorder.scrub(err)
		}
		_, decisionErr := decideAct(ctx, tool.client, snap, buildActSpace(snap.Actions), params.Goal, history)
		recorder.latest(func(d *probeDecision) {
			d.Disposition = "observed_only"
			if decisionErr != nil {
				d.ValidationError = recorder.scrub(decisionErr)
			}
		})
		// API errors are retained, but cannot provide useful downstream samples this trial.
		recorder.mu.Lock()
		last := recorder.trial.Decisions[len(recorder.trial.Decisions)-1]
		recorder.mu.Unlock()
		if last.ClassifierError != "" {
			return "error", last.ClassifierError
		}
		if truth.Success {
			return "coverage_complete", "Reached manual terminal outcome; no model choices executed"
		}
		if err := probeAdvance(ctx, tool, browser, params); err != nil {
			return "error", recorder.scrub(err)
		}
		recorder.latest(func(d *probeDecision) { d.OracleAdvanced = true })
		choice := truth.Expected[0]
		text := ""
		if choice.Operation == "TYPE_TEXT" {
			text = params.Values[choice.Label]
		}
		history = append(history, actHistoryEntry{Action: choice.Label, Operation: choice.Operation, Text: text, PageChanged: true})
	}
	return "max_steps", "Coverage action budget reached"
}

func TestBrowserActRealJevProbe(t *testing.T) {
	if os.Getenv("BROWSER_ACT_PROBE") != "1" {
		t.Skip("opt in with BROWSER_ACT_PROBE=1; this test calls real Jev")
	}
	key := strings.TrimSpace(os.Getenv("JEV_API_KEY"))
	if key == "" {
		t.Fatal("opt-in probe requires JEV_API_KEY")
	}
	output := strings.TrimSpace(os.Getenv("BROWSER_ACT_PROBE_OUTPUT"))
	if output == "" {
		t.Fatal("opt-in probe requires BROWSER_ACT_PROBE_OUTPUT (JSON file path)")
	}
	repeats := 3
	if raw := os.Getenv("BROWSER_ACT_PROBE_RUNS"); raw != "" {
		var err error
		repeats, err = strconv.Atoi(raw)
		if err != nil || repeats < 1 || repeats > 100 {
			t.Fatal("BROWSER_ACT_PROBE_RUNS must be 1..100 (default 3)")
		}
	}
	modes := []string{"production", "coverage"}
	switch mode := os.Getenv("BROWSER_ACT_PROBE_MODE"); mode {
	case "", "both":
	case "production", "coverage":
		modes = []string{mode}
	default:
		t.Fatal("BROWSER_ACT_PROBE_MODE must be both, production or coverage")
	}
	model := strings.TrimSpace(os.Getenv("JEV_MODEL"))
	if model == "" {
		model = "jev-latest"
	}
	report := &probeReport{SchemaVersion: 1, StartedAt: time.Now().UTC(), Model: model, Repeats: repeats, Modes: modes, Medium: actMediumConfidence, High: actHighConfidence, MaxUncertain: actMaxUncertain, MaxSteps: 20, TimeoutMS: actMaxTimeout.Milliseconds(), Trials: []*probeTrial{}, Semantics: "Manual oracle operation/target correctness is independent of confidence and final DOM success. Production uses the unchanged loop/gates. Coverage observes all heads but executes ONLY manual fixture choices, even after low confidence or wrong picks; final_success is oracle-assisted, autonomous_success=null. Null confidence/correctness means absent/not applicable/unlabelled. No threshold recommendations are inferred. complete=false is a partial checkpoint."}
	report.Viewport = "1120x780@1"
	report.FixtureSHA256 = map[string]string{}
	entries, err := os.ReadDir("testdata/browser_act_probe")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("testdata/browser_act_probe", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		report.FixtureSHA256[entry.Name()] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	checkpoint := func() error { return probeWriteReport(output, report) }
	if err := checkpoint(); err != nil {
		t.Fatal(err)
	}
	binary := strings.TrimSpace(os.Getenv("BROWSER_ACT_PROBE_CHROME"))
	if binary == "" {
		var found bool
		binary, found = launcher.LookPath()
		if !found {
			t.Fatal("opt-in probe requires installed Chrome (or BROWSER_ACT_PROBE_CHROME); no automatic download")
		}
	}
	launch := launcher.New().Bin(binary).Headless(true).UserDataDir(t.TempDir()).NoSandbox(true)
	controlURL, err := launch.Launch()
	if err != nil {
		t.Fatalf("launch Chrome: %v", err)
	}
	defer launch.Cleanup()
	chrome := rod.New().ControlURL(controlURL)
	if err := chrome.Connect(); err != nil {
		t.Fatalf("connect Chrome: %v", err)
	}
	defer chrome.Close()
	version, err := (proto.BrowserGetVersion{}).Call(chrome)
	if err != nil {
		t.Fatalf("Chrome version: %v", err)
	}
	report.ChromeVersion = version.Product + " " + version.Revision
	fixtures := httptest.NewServer(http.FileServer(http.Dir("testdata/browser_act_probe")))
	defer fixtures.Close()
	upstream := jev.NewClient(key, model, os.Getenv("JEV_BASE_URL"))
	for repeat := 1; repeat <= repeats; repeat++ {
		for _, fixture := range probeFixtures {
			for _, mode := range modes {
				t.Run(fmt.Sprintf("%s/%s/%02d", mode, fixture.ID, repeat), func(t *testing.T) {
					trial := &probeTrial{Fixture: fixture, Repeat: repeat, Mode: mode, Status: "in_progress", Decisions: []probeDecision{}}
					report.Trials = append(report.Trials, trial)
					if err := checkpoint(); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), actMaxTimeout)
					defer cancel()
					page, err := chrome.Context(ctx).Page(proto.TargetCreateTarget{URL: "about:blank"})
					if err != nil {
						trial.Status, trial.Error = "error", err.Error()
						_ = checkpoint()
						t.Error(err)
						return
					}
					defer page.Close()
					if err = page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 1120, Height: 780, DeviceScaleFactor: 1}); err == nil {
						err = page.Navigate(fixtures.URL + "/" + fixture.ID + ".html")
					}
					if err == nil {
						err = page.WaitLoad()
					}
					if err != nil {
						trial.Status, trial.Error = "error", err.Error()
						_ = checkpoint()
						t.Error(err)
						return
					}
					browser := &rodActBrowser{page: page}
					recorder := &probeRecorder{trial: trial, secret: key, checkpoint: checkpoint, truth: func(ctx context.Context) (probeTruth, error) { return probeReadTruth(ctx, browser) }}
					proxy := httptest.NewServer(recorder.handler(upstream))
					defer proxy.Close()
					tool := &BrowserActTool{client: jev.NewClient("probe-loopback", model, proxy.URL)}
					params := actParams{Goal: fixture.Goal, Values: fixture.Values, MaxSteps: report.MaxSteps}
					if mode == "production" {
						run := tool.run(ctx, &probeBrowser{actBrowser: browser, recorder: recorder}, params)
						// Drain cancelled/in-flight handlers before final trial writes/checkpoints.
						proxy.Close()
						trial.Status, trial.Note, trial.ExecutedSteps = run.Status, recorder.scrub(errors.New(run.Note)), run.Steps
						recorder.latest(func(d *probeDecision) {
							if d.Disposition == "not_executed" {
								d.Disposition = run.Status
							}
						})
					} else {
						status, note := probeCoverage(ctx, tool, browser, recorder, params)
						proxy.Close()
						trial.Status, trial.Note = status, note
					}
					// Independent bounded context verifies even a time-budget stop, without model calls.
					verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
					trial.FinalTruth, err = probeReadTruth(verifyCtx, browser)
					verifyCancel()
					if err != nil {
						trial.Error = recorder.scrub(err)
					} else {
						trial.FinalSuccess = trial.FinalTruth.Success
					}
					if mode == "production" {
						trial.AutonomousSuccess = probeBool(trial.FinalSuccess)
					}
					recorder.mu.Lock()
					writeErr := recorder.writeError
					recorder.mu.Unlock()
					if err := checkpoint(); err != nil {
						t.Error(err)
					}
					if writeErr != nil {
						t.Error(writeErr)
					}
					t.Logf("status=%s final_success=%v decisions=%d output=%s", trial.Status, trial.FinalSuccess, len(trial.Decisions), output)
					// Confidence stops, wrong picks, and false DONE are data, not assertions to suppress.
					// Infrastructure failures fail the command but never abort later fixture trials.
					if trial.Status == "error" || trial.Error != "" {
						t.Errorf("probe infrastructure/classifier/execution error: %s %s", trial.Note, trial.Error)
					}
				})
			}
		}
	}
	report.Complete = true
	if err := checkpoint(); err != nil {
		t.Fatal(err)
	}
}

// Offline checks guard the recorder itself: target-vs-operation confidence, wrong high-confidence
// picks, low-confidence stops, malformed distributions and terminal claims must survive in JSON.
func TestBrowserActProbeRecorder(t *testing.T) {
	for _, tc := range []struct {
		name, operation, target string
		confidence              float64
		status                  string
		correct                 bool
	}{
		{"low_confidence_rejected", "CLICK", "2", 0.4, "low_confidence", true},
		{"high_confidence_wrong_target", "CLICK", "1", 0.97, "max_steps", false},
		{"false_done", "DONE", "", 0.97, "done", false},
		{"blocked", "BLOCKED", "", 0.3, "blocked", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trial := &probeTrial{Decisions: []probeDecision{}}
			recorder := &probeRecorder{trial: trial, truth: func(context.Context) (probeTruth, error) {
				return probeTruth{State: "ready", Expected: []probeChoice{{Operation: "CLICK", Label: "Search"}}}, nil
			}}
			proxy := httptest.NewServer(recorder.handler(fakeJev(t, []fakeDecision{{Operation: tc.operation, Target: tc.target, Confidence: tc.confidence}})))
			defer proxy.Close()
			browser := &fakeActBrowser{snapshots: []*actSnapshot{actFixtureSnapshot("a", actSearchField, actSearchOpen, actSubmit)}}
			tool := &BrowserActTool{client: jev.NewClient("local", "fake", proxy.URL)}
			run := tool.run(context.Background(), &probeBrowser{actBrowser: browser, recorder: recorder}, actParams{Goal: "Submit search", MaxSteps: 1})
			if run.Status != tc.status {
				t.Fatalf("status=%s want %s", run.Status, tc.status)
			}
			recorder.latest(func(d *probeDecision) {
				if d.Disposition == "not_executed" {
					d.Disposition = run.Status
				}
			})
			if len(trial.Decisions) != 1 {
				t.Fatalf("lost decision: %+v", trial.Decisions)
			}
			d := trial.Decisions[0]
			if !d.Valid || d.OperationConfidence == nil || *d.OperationConfidence != tc.confidence {
				t.Fatalf("lost confidence: %+v", d)
			}
			if d.DecisionCorrect == nil || *d.DecisionCorrect != tc.correct {
				t.Fatalf("wrong semantic grade: %+v", d)
			}
			if tc.status == "low_confidence" && (len(run.Steps) != 0 || d.Disposition != "low_confidence") {
				t.Fatalf("rejected decision executed/lost: %+v", d)
			}
			if tc.operation == "CLICK" && (d.TargetConfidence == nil || d.TargetLabel == "") {
				t.Fatalf("missing target head: %+v", d)
			}
			if tc.operation == "DONE" && d.TargetConfidence != nil {
				t.Fatal("DONE must not invent target confidence")
			}
			output := filepath.Join(t.TempDir(), "report.json")
			if err := probeWriteReport(output, &probeReport{Trials: []*probeTrial{trial}}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var decoded probeReport
			if err = json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Trials[0].Decisions) != 1 {
				t.Fatal("decision lost in JSON")
			}
		})
	}
}

func TestBrowserActProbeDecode(t *testing.T) {
	space := buildActSpace([]actAction{actSearchField, actSearchOpen, actSubmit})
	questions, _ := buildActQuestions(space, "Submit")
	for _, tc := range []struct {
		name                                  string
		operationConfidence, targetConfidence float64
		malformed                             bool
	}{
		{"weaker_target", 0.99, 0.2, false}, {"weaker_operation", 0.2, 0.99, false}, {"malformed", 0.99, 0.99, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operation := jev.Answer{Type: "choice", Choice: "CLICK", Confidence: tc.operationConfidence, Probabilities: map[string]float64{}}
			_, ops := buildActQuestions(space, "Submit")
			for key := range ops {
				operation.Probabilities[key] = 0.1 / float64(len(ops)-1)
			}
			operation.Probabilities["CLICK"] = 0.9
			target := jev.Answer{Type: "choice", Choice: "2", Confidence: tc.targetConfidence, Probabilities: map[string]float64{"1": 0.1, "2": 0.9}}
			if tc.malformed {
				target.Probabilities["2"] = 0.2
			}
			d := probeDecision{Request: jev.SystemOneRequest{Questions: questions}, Response: &jev.SystemOneResponse{Answers: map[string]jev.Answer{"operation": operation, "click_target": target}}, Truth: probeTruth{Expected: []probeChoice{{Operation: "CLICK", Label: "Search"}}}}
			probeDecode(&d)
			if tc.malformed {
				if d.Valid || d.ValidationError == "" || d.Response == nil {
					t.Fatalf("malformed response lost/accepted: %+v", d)
				}
				return
			}
			if !d.Valid || d.MinimumConfidence == nil || *d.MinimumConfidence != 0.2 || d.Band != "below_medium" {
				t.Fatalf("wrong weakest-head confidence: %+v", d)
			}
			if d.OperationProbability == nil || *d.OperationProbability != 0.9 || d.TargetProbability == nil || *d.TargetProbability != 0.9 {
				t.Fatalf("probability conflated with confidence: %+v", d)
			}
		})
	}
}

func TestBrowserActProbeClassifierError(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sentinel-secret: upstream failed", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	trial := &probeTrial{Decisions: []probeDecision{}}
	recorder := &probeRecorder{trial: trial, secret: "sentinel-secret"}
	proxy := httptest.NewServer(recorder.handler(jev.NewClient("sentinel-secret", "fake", bad.URL)))
	defer proxy.Close()
	_, err := jev.NewClient("local", "fake", proxy.URL).SystemOne(context.Background(), jev.SystemOneRequest{})
	if err == nil || len(trial.Decisions) != 1 || trial.Decisions[0].ClassifierError == "" {
		t.Fatalf("error decision lost: %+v", trial)
	}
	raw, _ := json.Marshal(trial)
	if bytes.Contains(raw, []byte("sentinel-secret")) {
		t.Fatal("secret leaked into report")
	}
}
