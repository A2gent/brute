package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func addScreenshotStep(sess *session.Session, images ...string) {
	id := fmt.Sprintf("capture-%d", len(sess.Messages))
	sess.AddAssistantMessage("capture", []session.ToolCall{{ID: id, Name: "browser_chrome", Input: json.RawMessage(`{}`)}})
	raw := make([]interface{}, len(images))
	for i, data := range images {
		raw[i] = map[string]interface{}{"media_type": "image/png", "data_base64": data}
	}
	sess.AddToolResult([]session.ToolResult{{ToolCallID: id, Name: "browser_chrome", Content: "captured", Metadata: map[string]interface{}{"admission_images": raw, "image_inline": raw[0]}}})
}

func requestImageData(req *llm.ChatRequest) []string {
	var data []string
	for _, msg := range req.Messages {
		for _, img := range msg.Images {
			data = append(data, img.DataBase64)
		}
	}
	return data
}

func screenshotAgent(t *testing.T, cfg Config) *Agent {
	t.Helper()
	return New(cfg, nil, tools.NewManager(t.TempDir()), nil)
}

func TestScreenshotRetentionLatestNAndUserImages(t *testing.T) {
	for _, n := range []int{0, 1, 3, 10} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			sess := session.New("vision")
			sess.AddUserMessageWithImages("inspect", []session.ImageAttachment{{MediaType: "image/png", DataBase64: "user-old"}})
			addScreenshotStep(sess, "one", "two")
			addScreenshotStep(sess, "three")
			sess.AddUserMessageWithImages("compare", []session.ImageAttachment{{DataBase64: "user-latest-1"}, {DataBase64: "user-latest-2"}})
			addScreenshotStep(sess, "four")
			req := screenshotAgent(t, Config{ScreenshotKeepLatest: n}).buildRequest(sess)
			latest := n
			if latest == 0 {
				latest = 2
			}
			screenshots := []string{"one", "two", "three", "four"}
			want := map[string]bool{"user-old": true, "user-latest-1": true, "user-latest-2": true}
			for _, data := range screenshots[max(0, len(screenshots)-latest):] {
				want[data] = true
			}
			got := map[string]bool{}
			for _, data := range requestImageData(req) {
				got[data] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("images = %v, want %v", got, want)
			}
			stubCount := 0
			for _, msg := range req.Messages {
				stubCount += strings.Count(msg.Content, "omitted; re-capture if needed]")
				for _, tr := range msg.ToolResults {
					if tr.Metadata["admission_images"] != nil || tr.Metadata["image_inline"] != nil {
						t.Fatal("outgoing tool metadata still contains images")
					}
				}
			}
			if stubCount != max(0, 4-latest) {
				t.Fatalf("stubs = %d", stubCount)
			}
			if len(sess.Messages[2].ToolResults[0].Metadata["admission_images"].([]interface{})) != 2 {
				t.Fatal("retention modified the original screenshots")
			}
		})
	}
}

func TestScreenshotRetentionStickyStubSurvivesSaveAndReload(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("vision")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("inspect")
	addScreenshotStep(sess, "one")
	// Save before aging out so the next save must update a historical message.
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	addScreenshotStep(sess, "two", "three")
	ag := screenshotAgent(t, Config{})
	first := ag.buildRequest(sess)
	prefix, _ := json.Marshal(first.Messages[:4])
	if !bytes.Contains(prefix, []byte("[screenshot from step 1 omitted; re-capture if needed]")) {
		t.Fatalf("no stable step stub in %s", prefix)
	}
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	sess, err = session.NewManager(store).Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A new task and an opted-in but unavailable classifier must not re-decide.
	sess.AddUserMessage("now inspect something else")
	ag = screenshotAgent(t, Config{ScreenshotGate: &ScreenshotGate{}})
	second, _ := json.Marshal(ag.buildRequest(sess).Messages[:4])
	if !bytes.Equal(prefix, second) {
		t.Fatalf("sticky prefix changed:\n%s\n%s", prefix, second)
	}
}

func TestScreenshotRetentionJevBatchedAndSticky(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(buf.Bytes())
	for _, mode := range []string{"scores", "high_threshold", "threshold_boundary", "error", "uncertain", "invalid_probability", "missing", "partial_batch_error"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req jev.SystemOneRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if r.Header.Get("TypeSafe-Danger-Zone-Images") == "" || len(req.Assets) != len(req.Questions) || !strings.Contains(req.StateString(), "inspect checkout") {
					t.Errorf("invalid image relevance request: %+v", req)
				}
				if mode == "error" || (mode == "partial_batch_error" && calls == 2) {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				answers := map[string]interface{}{}
				for key, q := range req.Questions {
					if q.Type != "noul" || !strings.Contains(q.InstructionsString(), key) {
						t.Errorf("invalid question: %+v", q)
					}
					p := 0.1
					if key == "screenshot_1" {
						p = 0.9
					}
					if mode == "uncertain" {
						p = 0.5
					}
					if mode == "invalid_probability" {
						p = -0.1
					}
					if mode != "missing" {
						answers[key] = map[string]interface{}{"type": "noul", "noul": p}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"answers": answers})
			}))
			defer server.Close()
			sess := session.New("vision")
			sess.AddUserMessage("inspect checkout")
			count := 4
			if mode == "partial_batch_error" {
				count = 7
			}
			for i := 0; i < count; i++ {
				addScreenshotStep(sess, data)
			}
			threshold := 0.5
			if mode == "high_threshold" {
				threshold = 0.95
			} else if mode == "threshold_boundary" {
				threshold = 0.9
			}
			ag := screenshotAgent(t, Config{ScreenshotGate: &ScreenshotGate{Client: jev.NewClient("test", "", server.URL), Threshold: threshold}})
			first := ag.buildRequest(sess)
			want := count
			if mode == "scores" || mode == "threshold_boundary" {
				want = 3 // One relevant old image plus the latest two.
			} else if mode == "high_threshold" {
				want = 2
			}
			if got := len(requestImageData(first)); got != want {
				t.Fatalf("images = %d, want %d", got, want)
			}
			wantCalls := 1
			if mode == "partial_batch_error" {
				wantCalls = 2
			}
			encoded, err := json.Marshal(sess)
			if err != nil {
				t.Fatal(err)
			}
			sess = &session.Session{}
			if err := json.Unmarshal(encoded, sess); err != nil {
				t.Fatal(err)
			}
			sess.AddUserMessage("different task")
			second := ag.buildRequest(sess)
			if calls != wantCalls || !reflect.DeepEqual(first.Messages, second.Messages[:len(first.Messages)]) {
				t.Fatalf("re-decided old images: calls=%d", calls)
			}
		})
	}
}

func TestScreenshotRetentionMissingJevKeepsImages(t *testing.T) {
	sess := session.New("vision")
	sess.AddUserMessage("inspect")
	addScreenshotStep(sess, "one", "two", "three", "four")
	req := screenshotAgent(t, Config{ScreenshotGate: &ScreenshotGate{}}).buildRequest(sess)
	if len(requestImageData(req)) != 4 {
		t.Fatal("missing Jev credentials did not fail open")
	}
}

func TestScreenshotRetentionPreservesPathOnlyInlineImage(t *testing.T) {
	sess := session.New("vision")
	sess.AddUserMessage("inspect camera")
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "camera", Name: "take_camera_photo", Input: json.RawMessage(`{}`)}})
	sess.AddToolResult([]session.ToolResult{{ToolCallID: "camera", Name: "take_camera_photo", Metadata: map[string]interface{}{
		"image_inline": map[string]interface{}{"media_type": "image/jpeg", "path": "/camera/photo.jpg", "max_bytes": float64(2 * 1024 * 1024)},
		"image_file":   map[string]interface{}{"path": "/camera/photo.jpg"},
	}}})
	req := screenshotAgent(t, Config{}).buildRequest(sess)
	if len(requestImageData(req)) != 0 {
		t.Fatal("path-only image was normalized to an empty inline image")
	}
	inline, _ := req.Messages[2].ToolResults[0].Metadata["image_inline"].(map[string]interface{})
	if inline["path"] != "/camera/photo.jpg" {
		t.Fatal("provider cannot resolve its path-backed image")
	}
}
