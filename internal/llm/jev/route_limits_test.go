package jev

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

const routeOK = `{"model":"jev-1.14.0","answers":{"route":{"type":"choice","choice":"1","confidence":0.9}},"usage":{"input_tokens":1}}`

func routerMessage(prompt string, images ...llm.Image) *llm.ChatRequest {
	return &llm.ChatRequest{Messages: []llm.Message{{
		Role:    "user",
		Content: `Rules: [{"index":1,"match":"coding","target":"a"},{"index":2,"match":"docs","target":"b"}]` + "\n\nUser prompt: " + prompt,
		Images:  images,
	}}}
}

func pngImage(t *testing.T, w, h int) llm.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return llm.Image{Name: "shot.png", MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes())}
}

func TestRouteRetriesWithShorterStateOnMaxTokensExceeded(t *testing.T) {
	t.Parallel()
	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req SystemOneRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		state, _ := req.State.(string)
		sizes = append(sizes, len(state))
		if len(state) > 10_000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":{"error_type":"max_tokens_exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(routeOK))
	}))
	defer server.Close()

	resp, err := NewClient("k", "", server.URL+"/v1").Chat(context.Background(), routerMessage(strings.Repeat("я", 16_000)))
	if err != nil {
		t.Fatalf("Chat: %v (sizes %v)", err, sizes)
	}
	if !strings.Contains(resp.Content, `"index":1`) || len(sizes) != 3 || sizes[2] > 10_000 {
		t.Fatalf("content %s, attempt sizes %v", resp.Content, sizes)
	}
}

func TestRouteGivesUpOnPersistentMaxTokensExceeded(t *testing.T) {
	t.Parallel()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":{"error_type":"max_tokens_exceeded"}}`))
	}))
	defer server.Close()

	_, err := NewClient("k", "", server.URL+"/v1").Chat(context.Background(), routerMessage(strings.Repeat("x", 64_000)))
	if err == nil || !strings.Contains(err.Error(), "max_tokens_exceeded") || calls != maxRouteAttempts {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

func TestRouteSendsResizedImagesWithDangerZoneHeader(t *testing.T) {
	t.Parallel()
	var req SystemOneRequest
	var header string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get(imagesHeader)
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = w.Write([]byte(routeOK))
	}))
	defer server.Close()

	_, err := NewClient("k", "", server.URL+"/v1").Chat(context.Background(),
		routerMessage("fix this layout", pngImage(t, 2400, 100), llm.Image{Name: "broken", DataBase64: "bm90IGFuIGltYWdl"}))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if header != "true" || len(req.Assets) != 1 {
		t.Fatalf("header %q, assets %d (undecodable image must be skipped)", header, len(req.Assets))
	}
	state, _ := json.Marshal(req.State)
	for ref, asset := range req.Assets {
		if !strings.Contains(string(state), ref) || asset.Format != "jpg" {
			t.Fatalf("state %s does not reference %s, format %s", state, ref, asset.Format)
		}
		raw, _ := base64.StdEncoding.DecodeString(asset.DataBase64)
		cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || cfg.Width != imageMaxSide || cfg.Height != imageMinSide {
			t.Fatalf("resized to %dx%d (%v), want %dx%d", cfg.Width, cfg.Height, err, imageMaxSide, imageMinSide)
		}
	}
}

func TestRouteFallsBackToTextWhenImagesRejected(t *testing.T) {
	t.Parallel()
	var withAssets []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req SystemOneRequest
		_ = json.Unmarshal(body, &req)
		withAssets = append(withAssets, len(req.Assets) > 0)
		if len(req.Assets) > 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"image rejected"}`))
			return
		}
		if req.State != "fix it" {
			t.Errorf("text-only state = %#v", req.State)
		}
		_, _ = w.Write([]byte(routeOK))
	}))
	defer server.Close()

	if _, err := NewClient("k", "", server.URL+"/v1").Chat(context.Background(), routerMessage("fix it", pngImage(t, 300, 300))); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(withAssets) != 2 || !withAssets[0] || withAssets[1] {
		t.Fatalf("attempts with assets = %v", withAssets)
	}
}
