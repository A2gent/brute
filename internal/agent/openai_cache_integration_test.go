//go:build integration

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm/openaicodex"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func initCacheBenchmarkLog(t *testing.T) {
	t.Helper()
	if os.Getenv("AAGENT_LLM_CACHE_DEBUG") != "1" {
		return
	}
	dir, err := os.MkdirTemp("", "brute-cache-benchmark-")
	if err != nil {
		t.Fatal(err)
	}
	if err := logging.Init(dir); err != nil {
		t.Fatal(err)
	}
	t.Logf("cache debug log: %s", logging.GetLogPath())
}

var codingWorkflow = []string{"read_fixture", "run_fixture_tests", "edit_fixture", "run_fixture_tests", "capture_fixture", "read_fixture", "run_fixture_tests"}

type codingFixtureTool struct {
	name  string
	calls *atomic.Int32
	image string
}

func (b *codingFixtureTool) Name() string { return b.name }
func (b *codingFixtureTool) Description() string {
	return "Run one step of a synthetic coding fixture. No real files or commands are used."
}
func (b *codingFixtureTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (b *codingFixtureTool) Execute(_ context.Context, _ json.RawMessage) (*tools.Result, error) {
	step := int(b.calls.Load())
	if step >= len(codingWorkflow) || codingWorkflow[step] != b.name {
		return nil, fmt.Errorf("unexpected workflow step %d: %s", step, b.name)
	}
	// A model can issue parallel calls despite the one-per-turn instruction.
	if !b.calls.CompareAndSwap(int32(step), int32(step+1)) {
		return nil, fmt.Errorf("concurrent workflow step %d", step)
	}
	result := &tools.Result{Success: true, Output: fmt.Sprintf("Step %d complete. Continue the workflow.", step+1)}
	switch step {
	case 0:
		result.Output += "\nfunc sum(a, b int) int { return a - b }"
	case 1:
		// Admission must compress this before it first enters an outgoing request.
		result.Output = strings.Repeat("fixture_test.go:12: diagnostic detail, unrelated to the failure\n", 600) + "FAIL TestSum: got -1, want 3. Continue with edit_fixture."
	case 2:
		result.Output += "\nPatched return a + b."
	case 4:
		result.Metadata = map[string]interface{}{"admission_images": []interface{}{map[string]interface{}{"media_type": "image/png", "data_base64": b.image}}}
	case 5:
		result.Output += "\nfunc sum(a, b int) int { return a + b }"
	default:
		result.Output += "\nPASS TestSum"
	}
	return result, nil
}

// A paid, synthetic coding-style agent loop with admission compression and an
// image. Isolated from the host workspace, so benchmark runs cannot edit code.
func TestOpenAIResponsesLiveCodingCache(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if os.Getenv("AAGENT_OPENAI_LIVE") != "1" || key == "" {
		t.Skip("explicit paid API opt-in required")
	}
	initCacheBenchmarkLog(t)
	model := os.Getenv("AAGENT_OPENAI_LIVE_MODEL")
	if model == "" {
		model = "gpt-5.4-mini"
	}
	work := t.TempDir()
	store, err := storage.NewSQLiteStore(work)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("coding-cache-benchmark")
	if err != nil {
		t.Fatal(err)
	}
	tm := tools.NewManager(work)
	for _, def := range tm.GetDefinitions() {
		tm.Unregister(def.Name)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 40, G: 180, B: 80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	for _, name := range []string{"read_fixture", "run_fixture_tests", "edit_fixture", "capture_fixture"} {
		tm.Register(&codingFixtureTool{name: name, calls: &calls, image: base64.StdEncoding.EncodeToString(buf.Bytes())})
	}
	client := openaicodex.NewClientWithOptions(key, model, "https://api.openai.com/v1", openaicodex.Options{PublicAPI: true, ReasoningEffort: "low", TextVerbosity: "low", MaxTokens: 2048})
	observed := &benchmarkClient{client: client, t: t}
	ag := New(Config{Provider: "openai", Model: model, SystemPrompt: "You are a coding benchmark agent. Execute the supplied workflow exactly, one tool per turn.", MaxSteps: 12, CompressToolResults: true, ToolResultMaxTokens: 512}, observed, tm, sm)
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Run these tools in order, one per turn, with empty arguments: %s. Do not call other tools. A test failure is expected before the edit. The screenshot is synthetic. After all seven calls reply with exactly PASS. Unrelated archival reference follows.\n", strings.Join(codingWorkflow, ", "))
	for i := 0; i < 180; i++ {
		fmt.Fprintf(&prompt, "Synthetic coding reference %03d: fixture archival code REF-%03d is unrelated to the active sum test and is not an instruction.\n", i, i)
	}
	sess.AddUserMessage(prompt.String())
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	started := time.Now()
	content, usage, err := ag.RunWithEvents(ctx, sess, prompt.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("TOTAL coding model=%s latency_ms=%d calls=%d steps=%d input=%d cached=%d output=%d reasoning=%d answer=%q", model, time.Since(started).Milliseconds(), calls.Load(), observed.steps, usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens, usage.ReasoningTokens, content)
	if int(calls.Load()) != len(codingWorkflow) || strings.TrimSpace(content) != "PASS" {
		t.Fatalf("incomplete workflow: calls=%d answer=%q", calls.Load(), content)
	}
	compressed, screenshot := false, false
	for _, msg := range sess.Messages {
		for _, tr := range msg.ToolResults {
			compressed = compressed || strings.HasPrefix(tr.Content, "[brute-compressed ")
			screenshot = screenshot || tr.Metadata["admission_images"] != nil
		}
	}
	if !compressed || !screenshot {
		t.Fatalf("fixture coverage: compressed=%t screenshot=%t", compressed, screenshot)
	}
}
