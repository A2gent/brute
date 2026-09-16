package http

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/tools"
)

func TestLocalCompletionSelectionNeverFallsBackToCloud(t *testing.T) {
	for _, engine := range []string{"kokoro", "qwen3_tts", "piper_tts", "macos_say_tts"} {
		t.Run(engine, func(t *testing.T) {
			t.Setenv("AAGENT_TTS_ENGINE", engine)
			want := engine
			if engine == "kokoro" {
				want = "kokoro_tts"
			}
			got, err := completionSpeechTools("")
			if err != nil || !reflect.DeepEqual(got, []string{want}) {
				t.Fatalf("got %v, %v", got, err)
			}
			clips := speechcache.New(0)
			manager := tools.NewManager(".")
			cloud := &fakeSpeechTool{name: "edge_tts", store: clips}
			manager.Register(cloud)
			s := &Server{toolManager: manager, speechClips: clips}
			rec := httptest.NewRecorder()
			s.handleCompletionSpeech(rec, httptest.NewRequest("POST", "/speech/completion", strings.NewReader(`{"text":"hello"}`)))
			if rec.Code != http.StatusBadGateway || cloud.calls != 0 {
				t.Fatalf("unexpected fallback: %d calls=%d", rec.Code, cloud.calls)
			}
		})
	}
}

func TestLocalCompletionRoutesRequestedModel(t *testing.T) {
	for _, model := range []string{"kokoro", "qwen3_tts"} {
		t.Run(model, func(t *testing.T) {
			clips := speechcache.New(0)
			manager := tools.NewManager(".")
			name := model
			if model == "kokoro" {
				name = "kokoro_tts"
			}
			fake := &fakeSpeechTool{name: name, store: clips}
			manager.Register(fake)
			s := &Server{toolManager: manager, speechClips: clips}
			rec := httptest.NewRecorder()
			s.handleCompletionSpeech(rec, httptest.NewRequest("POST", "/speech/completion", strings.NewReader(`{"text":"hello","language":"en","model":"`+model+`"}`)))
			if rec.Code != 200 || fake.calls != 1 {
				t.Fatalf("%d: %s", rec.Code, rec.Body.String())
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(fake.params, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["language"] != "en" {
				t.Fatalf("missing language: %s", fake.params)
			}
		})
	}
}

func TestTranscriptionEngineSelection(t *testing.T) {
	runner := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nprintf '{\"text\":\"local transcript\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_SPEECH_PYTHON", runner)
	t.Setenv("AAGENT_STT_ENGINE", "")
	for _, engine := range []string{"", "parakeet", "moonshine", "invalid"} {
		t.Run(engine, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, _ := writer.CreateFormFile("audio", "recording.wav")
			_, _ = part.Write([]byte("fake wav"))
			_ = writer.WriteField("engine", engine)
			_ = writer.Close()
			req := httptest.NewRequest("POST", "/speech/transcribe", &body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			rec := httptest.NewRecorder()
			(&Server{}).handleTranscribeSpeech(rec, req)
			want := 200
			if engine == "invalid" {
				want = 400
			}
			if rec.Code != want {
				t.Fatalf("%d: %s", rec.Code, rec.Body.String())
			}
			if want == 200 && !strings.Contains(rec.Body.String(), "local transcript") {
				t.Fatal(rec.Body.String())
			}
		})
	}
}

func TestMeetingUsesDefaultLocalSTTEngine(t *testing.T) {
	runner := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nprintf '{\"text\":\"meeting transcript\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_SPEECH_PYTHON", runner)
	t.Setenv("AAGENT_STT_ENGINE", "parakeet")
	audio := filepath.Join(t.TempDir(), "meeting-microphone.wav")
	if err := os.WriteFile(audio, []byte("fake wav"), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := (&Server{}).transcribeMeetingAudio(context.Background(), meetingHistoryItem{AudioPaths: []string{audio}})
	if err != nil || !strings.Contains(text, "meeting transcript") {
		t.Fatalf("%q %v", text, err)
	}
}
