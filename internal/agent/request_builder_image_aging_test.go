package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestBuildRequestImageAgingAndRecovery(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := session.NewManager(store)
	folder := t.TempDir()
	manager.SetJSONLFolder(folder)
	sess, err := manager.Create("vision")
	if err != nil {
		t.Fatal(err)
	}
	// A real PNG header lets read recognize the saved image without text decoding.
	data := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZ1kAAAAASUVORK5CYII="
	old := session.ImageAttachment{Name: "../../extension.png", MediaType: "image/png", DataBase64: data}
	sess.AddUserMessageWithImages("inspect", []session.ImageAttachment{old})
	ag := New(Config{}, nil, tools.NewManager(t.TempDir()), manager)
	if got := requestImageData(ag.buildRequest(context.Background(), sess)); !reflect.DeepEqual(got, []string{data}) {
		t.Fatalf("newest image not kept: %v", got)
	}
	if _, err := os.Stat(filepath.Join(folder, sess.ID, "images")); !os.IsNotExist(err) {
		t.Fatalf("image was saved before omission was needed: %v", err)
	}
	sess.AddAssistantMessage("first answer", nil)
	sess.AddUserMessage("continue")
	if got := len(requestImageData(ag.buildRequest(context.Background(), sess))); got != 1 {
		t.Fatalf("image aged before threshold: %d", got)
	}
	sess.AddAssistantMessage("second answer", nil)
	latest := session.ImageAttachment{Name: "latest.png", MediaType: "image/png", DataBase64: data}
	sess.AddUserMessageWithImages("new screenshot", []session.ImageAttachment{latest})
	req := ag.buildRequest(context.Background(), sess)
	if got := len(requestImageData(req)); got != 1 {
		t.Fatalf("inline images after threshold = %d, want only latest", got)
	}
	if !strings.Contains(req.Messages[0].Content, "[screenshot omitted from context: ../../extension.png, saved at ") || len(req.Messages[0].Images) != 0 {
		t.Fatalf("missing old-image placeholder: %+v", req.Messages[0])
	}
	paths, err := filepath.Glob(filepath.Join(folder, sess.ID, "images", "*.png"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("saved images = %v, error = %v", paths, err)
	}
	if !strings.Contains(req.Messages[0].Content, paths[0]) {
		t.Fatal("placeholder does not point at the saved image")
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil || base64.StdEncoding.EncodeToString(raw) != data {
		t.Fatalf("saved image bytes differ: %v", err)
	}
	params, _ := json.Marshal(map[string]string{"path": paths[0]})
	result, err := tools.NewReadTool(t.TempDir()).Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("cannot reopen screenshot: result=%+v err=%v", result, err)
	}
	inline, _ := result.Metadata["image_inline"].(map[string]interface{})
	if inline["data_base64"] != data {
		t.Fatal("read did not return the saved screenshot as an image")
	}
	if !reflect.DeepEqual(sess.Messages[0].Images, []session.ImageAttachment{old}) {
		t.Fatal("aging mutated the original session attachment")
	}
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	reloaded, err := session.NewManager(store).Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ag.buildRequest(context.Background(), reloaded).Messages[0].Content; got != req.Messages[0].Content {
		t.Fatalf("placeholder changed after reload: %q", got)
	}
}

func TestBuildRequestImageAgeCountsAnswersNotToolSteps(t *testing.T) {
	sess := session.New("vision")
	sess.AddUserMessageWithImages("inspect", []session.ImageAttachment{{Name: "extension.png", DataBase64: "image"}})
	for range 3 {
		sess.AddAssistantMessage("working", []session.ToolCall{{ID: "step", Name: "read", Input: json.RawMessage(`{}`)}})
	}
	ag := screenshotAgent(t, Config{ScreenshotGate: &ScreenshotGate{}})
	if got := len(requestImageData(ag.buildRequest(context.Background(), sess))); got != 1 {
		t.Fatalf("tool steps expired image: %d", got)
	}
	sess.AddAssistantMessage("", nil) // A failed/empty response is not a completed answer.
	sess.AddAssistantMessage("answer one", nil)
	sess.AddAssistantMessage("  ", nil)
	if got := len(requestImageData(ag.buildRequest(context.Background(), sess))); got != 1 {
		t.Fatalf("empty responses expired image before two answers: %d", got)
	}
	sess.AddAssistantMessage("answer two", nil)
	req := ag.buildRequest(context.Background(), sess)
	if got := len(requestImageData(req)); got != 0 {
		t.Fatalf("user image bypassed aging with Jev enabled: %d", got)
	}
}

func TestBuildRequestToolImageAgesAfterAnswers(t *testing.T) {
	sess := session.New("vision")
	sess.AddUserMessage("inspect")
	addScreenshotStep(sess, "old")
	sess.AddAssistantMessage("first answer", nil)
	sess.AddAssistantMessage("second answer", nil)
	addScreenshotStep(sess, "latest")
	req := screenshotAgent(t, Config{}).buildRequest(context.Background(), sess)
	if got := requestImageData(req); !reflect.DeepEqual(got, []string{"latest"}) {
		t.Fatalf("tool-image aging = %v", got)
	}
}
