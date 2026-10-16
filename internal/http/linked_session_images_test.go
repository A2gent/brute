package http

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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
			attachment := func(name, typ string, seed uint8) session.ImageAttachment {
				t.Helper()
				var buf bytes.Buffer
				img := image.NewRGBA(image.Rect(0, 0, 2, 2))
				img.Set(0, 0, color.RGBA{R: seed, A: 255})
				var err error
				if typ == "image/jpeg" {
					err = jpeg.Encode(&buf, img, nil)
				} else {
					err = png.Encode(&buf, img)
				}
				if err != nil {
					t.Fatal(err)
				}
				return session.ImageAttachment{Name: name, MediaType: typ, DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes())}
			}
			original := attachment("reference.png", "image/png", 1)
			later := session.ImageAttachment{Name: "later.png", MediaType: "image/png", URL: "https://example.com/later.png"}
			screenshot := attachment("", "image/jpeg", 2)
			inline := attachment("", "image/png", 3)
			raw := attachment("", "image/png", 4)
			fresh := attachment("new.png", "image/png", 5)
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
				{Name: "chrome_extension", Content: `{"data_url":"data:image/png;base64,` + raw.DataBase64 + `"}`},
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
			hydrate := func(images []session.ImageAttachment) []session.ImageAttachment {
				t.Helper()
				var out []session.ImageAttachment
				for _, img := range images {
					loaded, err := img.Inline()
					if err != nil {
						t.Fatal(err)
					}
					loaded.Path, loaded.SHA256, loaded.Root = "", "", ""
					loaded.Width, loaded.Height = 0, 0
					out = append(out, loaded)
				}
				return out
			}
			if len(child.Messages) != 1 || !reflect.DeepEqual(hydrate(child.Messages[0].Images), want) {
				t.Fatalf("child images = %#v, want %#v", child.Messages, want)
			}
			if linkType != "review" {
				if strings.Contains(child.Messages[0].Content, raw.DataBase64) {
					t.Fatal("raw screenshot base64 leaked into compact text")
				}
				grandchild := create(child.ID, "continuation", nil)
				if !reflect.DeepEqual(hydrate(grandchild.Messages[0].Images), want) {
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
