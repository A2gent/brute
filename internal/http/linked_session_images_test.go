package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestCreateLinkedContinuationPreservesImages(t *testing.T) {
	for _, linkType := range []string{"", "continuation", "review"} {
		t.Run("link_type="+linkType, func(t *testing.T) {
			server, _ := newBruteHTTPProxyTestServer(t)
			parent, err := server.sessionManager.Create("build")
			if err != nil {
				t.Fatal(err)
			}
			original := session.ImageAttachment{Name: "reference.png", MediaType: "image/png", DataBase64: "cmVmZXJlbmNl"}
			later := session.ImageAttachment{Name: "later.png", MediaType: "image/png", URL: "https://example.com/later.png"}
			screenshot := session.ImageAttachment{MediaType: "image/jpeg", DataBase64: "c2NyZWVuc2hvdA=="}
			inline := session.ImageAttachment{MediaType: "image/png", DataBase64: "aW5saW5l"}
			raw := session.ImageAttachment{MediaType: "image/png", DataBase64: "cmF3"}
			fresh := session.ImageAttachment{Name: "new.png", MediaType: "image/png", DataBase64: "bmV3"}
			parent.AddUserMessageWithImages("Use this reference", []session.ImageAttachment{original})
			for i := 0; i < 15; i++ {
				parent.AddAssistantMessage("working", nil)
			}
			parent.AddUserMessageWithImages("Compare this", []session.ImageAttachment{later, original})
			parent.AddToolResult([]session.ToolResult{
				{Name: "browser_chrome", Content: "Screenshot captured", Metadata: map[string]interface{}{
					"admission_images": []interface{}{map[string]interface{}{"media_type": screenshot.MediaType, "data_base64": screenshot.DataBase64}},
					"image_inline":     map[string]interface{}{"media_type": screenshot.MediaType, "data_base64": screenshot.DataBase64},
				}},
				{Name: "take_camera_photo", Metadata: map[string]interface{}{
					"image_inline": map[string]interface{}{"media_type": inline.MediaType, "data_base64": inline.DataBase64},
				}},
				{Name: "chrome_extension", Content: `{"data_url":"data:image/png;base64,cmF3"}`},
			})
			parent.SetStatus(session.StatusFailed)
			if err := server.sessionManager.Save(parent); err != nil {
				t.Fatal(err)
			}

			create := func(parentID, kind string, uploads []MessageImagePayload) *session.Session {
				t.Helper()
				payload, err := json.Marshal(CreateSessionRequest{AgentID: "build", ParentID: parentID, LinkType: kind, Task: "go on", Queued: true, Images: uploads})
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				server.router.ServeHTTP(rec, httptest.NewRequest(stdhttp.MethodPost, "/sessions/", strings.NewReader(string(payload))))
				if rec.Code != stdhttp.StatusCreated {
					t.Fatalf("create child: %d %s", rec.Code, rec.Body.String())
				}
				var response CreateSessionResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				child, err := server.sessionManager.Get(response.ID)
				if err != nil {
					t.Fatal(err)
				}
				return child
			}

			child := create(parent.ID, linkType, []MessageImagePayload{{Name: fresh.Name, MediaType: fresh.MediaType, DataBase64: fresh.DataBase64}})
			want := []session.ImageAttachment{fresh}
			if linkType != "review" {
				want = append(want, original, later, screenshot, inline, raw)
			}
			if len(child.Messages) != 1 || !reflect.DeepEqual(child.Messages[0].Images, want) {
				t.Fatalf("child images = %#v, want %#v", child.Messages, want)
			}
			if linkType != "review" {
				if strings.Contains(child.Messages[0].Content, raw.DataBase64) {
					t.Fatal("raw screenshot base64 leaked into compact text")
				}
				grandchild := create(child.ID, "continuation", nil)
				if !reflect.DeepEqual(grandchild.Messages[0].Images, want) {
					t.Fatalf("second model switch lost images: %#v", grandchild.Messages[0].Images)
				}
			}
		})
	}
}

func TestLinkedContinuationImageOnlySource(t *testing.T) {
	parent := session.New("build")
	parent.AddUserMessageWithImages("", []session.ImageAttachment{{MediaType: "image/png", DataBase64: "YQ=="}})
	parent.AddUserMessage("go on")
	ctx := buildLinkedContinuationContext(parent, "continue")
	if ctx.SourceMessageID != parent.Messages[0].ID {
		t.Fatalf("source = %q, want image-only message %q", ctx.SourceMessageID, parent.Messages[0].ID)
	}
}

func TestLinkedContinuationImagesBypassTextBudgetAndDoNotMutateParent(t *testing.T) {
	parent := session.New("build")
	payload := strings.Repeat("YQ==", 10000)
	parent.AddUserMessageWithImages(strings.Repeat("text ", 20000), []session.ImageAttachment{{Name: "original.png", MediaType: "image/png", DataBase64: payload}})
	parent.AddToolResult([]session.ToolResult{{Name: "chrome_extension", Content: `{"data_url":"data:image/png;base64,` + payload + `"}`}})
	before, err := json.Marshal(parent)
	if err != nil {
		t.Fatal(err)
	}
	images := linkedContinuationImages(parent, []session.ImageAttachment{{Name: "renamed.png", MediaType: "image/png", DataBase64: payload}})
	if len(images) != 1 || images[0].DataBase64 != payload || images[0].Name != "renamed.png" {
		t.Fatal("image was truncated or duplicated")
	}
	ctx := buildLinkedContinuationContext(parent, "continue")
	if len([]rune(ctx.Prompt)) > linkedContinuationPromptLimit || strings.Contains(ctx.Prompt, "YQ==") {
		t.Fatal("image payload leaked into bounded text context")
	}
	images[0].Name = "child.png"
	after, err := json.Marshal(parent)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("building child context mutated the parent transcript")
	}
}
