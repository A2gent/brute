package http

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestOpenRouterSpeechFormat(t *testing.T) {
	for _, tc := range []struct {
		model, format, contentType string
	}{
		{"google/gemini-3.1-flash-tts-preview", "pcm", "audio/wav"},
		{"google/gemini-3.8-flash-tts", "pcm", "audio/wav"},
		{"openai/gpt-4o-mini-tts", "mp3", "audio/mpeg"},
		{"microsoft/mai-voice-2.1", "mp3", "audio/mpeg"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := openRouterSpeechResponseFormat(tc.model); got != tc.format {
				t.Fatalf("format = %q, want %q", got, tc.format)
			}
		})
	}
}

func TestWrapOpenRouterPCMAsWAV(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03, 0x04}
	wav := wrapOpenRouterPCMAsWAV(pcm)
	if len(wav) != 48 {
		t.Fatalf("WAV size = %d, want 48", len(wav))
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[12:16]) != "fmt " || string(wav[36:40]) != "data" {
		t.Fatalf("invalid WAV chunks: %q", wav[:44])
	}
	if got := binary.LittleEndian.Uint16(wav[20:22]); got != 1 {
		t.Fatalf("audio format = %d, want PCM (1)", got)
	}
	if got := binary.LittleEndian.Uint16(wav[22:24]); got != 1 {
		t.Fatalf("channels = %d, want mono", got)
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 24000 {
		t.Fatalf("sample rate = %d, want 24000", got)
	}
	if got := binary.LittleEndian.Uint16(wav[34:36]); got != 16 {
		t.Fatalf("bits per sample = %d, want 16", got)
	}
	if !bytes.Equal(wav[44:], pcm) {
		t.Fatalf("WAV payload = %v, want %v", wav[44:], pcm)
	}
}

func TestCompletionSpeechGeminiUsesPCMAndReturnsWAV(t *testing.T) {
	var gotBody map[string]any
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"google/gemini-3.1-flash-tts-preview","supported_voices":["Zephyr"]}]}`)), Request: req}, nil
		}
		if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"audio/pcm"}}, Body: io.NopCloser(strings.NewReader("\x01\x02\x03\x04")), Request: req}, nil
	})
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-key"}
	server := &Server{config: cfg, openRouterModelsClient: client}
	rec := httptest.NewRecorder()
	server.handleCompletionSpeech(rec, httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"hello","model":"openrouter:google/gemini-3.1-flash-tts-preview"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := gotBody["response_format"]; got != "pcm" {
		t.Fatalf("response_format = %#v, want pcm", got)
	}
	if got := gotBody["voice"]; got != "Zephyr" {
		t.Fatalf("voice = %#v, want Zephyr", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("Content-Type = %q, want audio/wav", got)
	}
	if body := rec.Body.Bytes(); len(body) != 48 || string(body[:4]) != "RIFF" || !bytes.Equal(body[44:], []byte{1, 2, 3, 4}) {
		t.Fatalf("invalid WAV response: %v", body)
	}
}

func TestCompletionSpeechNonGeminiPreservesMP3(t *testing.T) {
	var gotBody map[string]any
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"openai/gpt-4o-mini-tts","supported_voices":["alloy"]}]}`)), Request: req}, nil
		}
		if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"audio/mpeg"}}, Body: io.NopCloser(strings.NewReader("mp3")), Request: req}, nil
	})
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-key"}
	server := &Server{config: cfg, openRouterModelsClient: client}
	rec := httptest.NewRecorder()
	server.handleCompletionSpeech(rec, httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"hello","model":"openrouter:openai/gpt-4o-mini-tts"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d payload=%#v type=%q body=%q", rec.Code, gotBody, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("Content-Type = %q, want audio/mpeg", got)
	}
	if rec.Body.String() != "mp3" {
		t.Fatalf("body = %q, want mp3", rec.Body.String())
	}
}

func TestCompletionSpeechGeminiRejectsIncompletePCMSample(t *testing.T) {
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"google/gemini-3.1-flash-tts-preview","supported_voices":["Zephyr"]}]}`)), Request: req}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("\x01\x02\x03")), Request: req}, nil
	})
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-key"}
	server := &Server{config: cfg, openRouterModelsClient: client}
	rec := httptest.NewRecorder()
	server.handleCompletionSpeech(rec, httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"hello","model":"openrouter:google/gemini-3.1-flash-tts-preview"}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); strings.HasPrefix(contentType, "audio/") {
		t.Fatalf("invalid PCM returned as audio content type %q", contentType)
	}
}

func TestCompletionSpeechOpenRouterCatalogVoices(t *testing.T) {
	for _, tc := range []struct {
		name, model, voices, language, want string
	}{
		{"mai 2.1", "microsoft/mai-voice-2.1", `["cs-CZ-Grant:MAI-Voice-2.1","en-US-Harper:MAI-Voice-2.1"]`, "en", "en-US-Harper:MAI-Voice-2.1"},
		{"mai 2.1 flash", "microsoft/mai-voice-2.1-flash", `["cs-CZ-Grant:MAI-Voice-2.1-Flash","en-US-Harper:MAI-Voice-2.1-Flash"]`, "", "en-US-Harper:MAI-Voice-2.1-Flash"},
		{"qwen flash", "qwen/qwen-audio-3.0-tts-flash", `["loongjohn","longanhuan_v3.6"]`, "en", "loongjohn"},
		{"qwen plus", "qwen/qwen-audio-3.0-tts-plus", `["longanlingxin","longanlufeng"]`, "en", "longanlingxin"},
		{"voxtral", "mistralai/voxtral-mini-tts-2603", `["fr_marie_neutral","en_paul_neutral"]`, "en", "en_paul_neutral"},
		{"grok", "x-ai/grok-voice-tts-1.0", `["eve","ara","rex"]`, "en", "eve"},

		{"alloy preferred", "openai/test-tts", `["en-US-Test","alloy","ru-RU-Test"]`, "ru", "alloy"},
		{"alloy must be exact", "future/test-tts", `["af_alloy","en-US-Test"]`, "en", "en-US-Test"},
		{"null voices", "sesame/csm-1b", `null`, "en", ""},
		{"empty voices", "future/test-tts", `[]`, "en", ""},
		{"blank voices", "future/test-tts", `[null,"","   "]`, "en", ""},
		{"skip blank voices", "future/test-tts", `[null," ","  First:full-ID  ","Second"]`, "ru", "First:full-ID"},
		{"russian", "future/test-tts", `["en-US-Test","ru-RU-Test:Full-ID"]`, "ru", "ru-RU-Test:Full-ID"},
		{"case insensitive locale", "future/test-tts", `["en-US-Test","RU-ru-Test:Full-ID"]`, " Ru-rU ", "RU-ru-Test:Full-ID"},
		{"russian underscore voice", "future/test-tts", `["en-US-Test","RU_Test:Full-ID"]`, "ru-ru", "RU_Test:Full-ID"},
		{"omitted defaults english", "future/test-tts", `["ru-RU-Test","EN-us-Test:Full-ID"]`, "", "EN-us-Test:Full-ID"},
		{"auto defaults english", "future/test-tts", `["ru-RU-Test","en-US-Test:Full-ID"]`, " AUTO ", "en-US-Test:Full-ID"},
		{"no language match uses first", "future/test-tts", `["fr-FR-Test","en-US-Test"]`, "ru", "fr-FR-Test"},
		{"no english uses first", "future/test-tts", `["fr-FR-Test","ru-RU-Test"]`, "", "fr-FR-Test"},
		{"locale prefix not substring", "future/test-tts", `["ruth","ru-RU-Test"]`, "ru", "ru-RU-Test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gets, posts int
			var gotBody map[string]any
			client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "Bearer test-key" {
					t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
				}
				if req.Method == http.MethodGet && req.URL.Path == "/api/v1/models" {
					gets++
					if req.URL.Query().Get("output_modalities") != "speech" {
						t.Fatalf("catalog query = %s", req.URL.RawQuery)
					}
					// Null entries and an unrelated model must not affect the selected model.
					body := `{"data":[null,{"id":"other/tts","supported_voices":["alloy"]},{"id":"` + tc.model + `","supported_voices":` + tc.voices + `}]}`
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				}
				if req.Method != http.MethodPost || req.URL.Path != "/api/v1/audio/speech" {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
				}
				posts++
				if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"audio/mpeg"}}, Body: io.NopCloser(strings.NewReader("audio")), Request: req}, nil
			})
			cfg := config.DefaultConfig()
			cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-key"}
			server := &Server{config: cfg, openRouterModelsClient: client}
			body, err := json.Marshal(speechCompletionRequest{Text: "hello", Language: tc.language, Model: "openrouter:" + tc.model})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			server.handleCompletionSpeech(rec, httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(string(body))))
			if rec.Code != http.StatusOK || rec.Body.String() != "audio" {
				t.Fatalf("%d: %s", rec.Code, rec.Body.String())
			}
			voice, present := gotBody["voice"]
			if tc.want == "" {
				if present {
					t.Errorf("voice must be omitted, got %#v", voice)
				}
			} else if voice != tc.want {
				t.Errorf("voice = %#v, want %q", voice, tc.want)
			}
			if gets != 1 || posts != 1 {
				t.Errorf("catalog GETs = %d, speech POSTs = %d, want 1 each", gets, posts)
			}
			if gotBody["model"] != tc.model || gotBody["input"] != "hello" || gotBody["response_format"] != "mp3" {
				t.Errorf("payload = %#v", gotBody)
			}
		})
	}
}

func TestCompletionSpeechOpenRouterCatalogFailureDoesNotPost(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
		err              error
	}{
		{"http error", `{"error":"catalog unavailable"}`, "503", http.StatusServiceUnavailable, nil},
		{"invalid json", `{`, "parse", http.StatusOK, nil},
		{"network error", "", "catalog offline", 0, errors.New("catalog offline")},
		{"unknown model", `{"data":[null,{"id":"other/tts","supported_voices":["alloy"]}]}`, "not found", http.StatusOK, nil},
		{"empty catalog", `{"data":[]}`, "not found", http.StatusOK, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gets, posts int
			client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPost {
					posts++
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("audio")), Request: req}, nil
				}
				gets++
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})
			cfg := config.DefaultConfig()
			cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-key"}
			server := &Server{config: cfg, openRouterModelsClient: client}
			rec := httptest.NewRecorder()
			server.handleCompletionSpeech(rec, httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"hello","model":"openrouter:chosen/tts"}`)))
			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), tc.want) || !strings.Contains(rec.Body.String(), "chosen/tts") || !strings.Contains(rec.Body.String(), "catalog") {
				t.Errorf("expected actionable catalog error with model and %q, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
			if gets != 1 || posts != 0 {
				t.Errorf("GETs = %d, POSTs = %d, want 1 and 0", gets, posts)
			}
		})
	}
}

func TestCompletionSpeechOpenRouterUpstreamErrorContext(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
		for _, voice := range []string{"en-US-Harper:MAI-Voice-2.1", ""} {
			t.Run(http.StatusText(status)+"/"+voice, func(t *testing.T) {
				var posts int
				client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
					if req.Method == http.MethodGet {
						body := `{"data":[{"id":"chosen/tts","supported_voices":["` + voice + `"]}]}`
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
					}
					posts++
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("upstream rejection")), Request: req}, nil
				})
				cfg := config.DefaultConfig()
				cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-key"}
				server := &Server{config: cfg, openRouterModelsClient: client}
				rec := httptest.NewRecorder()
				server.handleCompletionSpeech(rec, httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"hello","model":"openrouter:chosen/tts"}`)))
				wantCode := "400"
				if status == http.StatusNotFound {
					wantCode = "404"
				}
				wantVoice := voice
				if voice == "" {
					wantVoice = "provider default"
				}
				for _, want := range []string{"chosen/tts", "voice", wantVoice, wantCode, "upstream rejection"} {
					if !strings.Contains(rec.Body.String(), want) {
						t.Errorf("error missing %q: %s", want, rec.Body.String())
					}
				}
				if rec.Code != http.StatusBadGateway || posts != 1 {
					t.Errorf("status = %d, POSTs = %d; no retries allowed", rec.Code, posts)
				}
			})
		}
	}
}
