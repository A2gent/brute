package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/jev"
)

func postAdblock(server *Server, origin, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/browser-extension/adblock/classify", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	server.handleAdblockClassify(recorder, request)
	return recorder
}

func TestAdblockClassifyRejectsBadRequests(t *testing.T) {
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{}, nil)
	tooMany := `{"host":"a.ee","items":[` + strings.TrimSuffix(strings.Repeat(`{"id":"x"},`, jev.RelevanceBatchSize+1), ",") + `]}`
	cases := []struct {
		name, origin, body string
		want               int
	}{
		{"website origin", "https://evil.example", `{"host":"a.ee","items":[{"id":"x"}]}`, http.StatusForbidden},
		{"sandboxed page origin", "null", `{"host":"a.ee","items":[{"id":"x"}]}`, http.StatusForbidden},
		{"no items", "", `{"host":"a.ee","items":[]}`, http.StatusBadRequest},
		{"too many items", "", tooMany, http.StatusBadRequest},
		{"System One not configured", "chrome-extension://abc", `{"host":"a.ee","items":[{"id":"x"}]}`, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := postAdblock(server, tc.origin, tc.body).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAdblockClassifyMapsAnswersToElementIDs(t *testing.T) {
	var sent string
	jevServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sent = string(raw)
		_ = json.NewEncoder(w).Encode(jev.SystemOneResponse{Answers: map[string]jev.Answer{
			"item_0": {Type: "choice", Choice: "ad", Confidence: 0.97},
			"item_1": {Type: "choice", Choice: "unknown", Confidence: 0.99},
		}})
	}))
	defer jevServer.Close()
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "key", BaseURL: jevServer.URL}, nil)

	recorder := postAdblock(server, "chrome-extension://abc", `{"host":"a.ee","items":[
		{"id":"#selector-a","tag":"div","text":"Buy now"},
		{"id":"#selector-b","tag":"div"},
		{"id":"#selector-c","tag":"div","text":"password: hunter2"}
	]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var got struct {
		Verdicts []adblockVerdict `json:"verdicts"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Verdicts) != 1 || got.Verdicts[0] != (adblockVerdict{ID: "#selector-a", Verdict: "ad", Confidence: 0.97}) {
		t.Fatalf("verdicts = %+v", got.Verdicts)
	}
	if !strings.Contains(sent, "Buy now") {
		t.Fatalf("element text missing from Jev request: %s", sent)
	}
	if strings.Contains(sent, "hunter2") {
		t.Fatalf("secret-looking text reached Jev: %s", sent)
	}
}

func TestAdblockSelectionOverridesGeneralSystemOneChoice(t *testing.T) {
	cases := []struct {
		name                   string
		settings               map[string]string
		wantBackend, wantModel string
	}{
		{"inherits general choice", map[string]string{systemOneBackendSettingKey: "lmstudio", systemOneModelSettingKey: "general"}, "lmstudio", "general"},
		{"own local model", map[string]string{systemOneBackendSettingKey: "jev", adblockBackendSettingKey: "lmstudio", adblockModelSettingKey: "qwen/qwen3-vl-4b"}, "lmstudio", "qwen/qwen3-vl-4b"},
		{"own Jev while general is local", map[string]string{systemOneBackendSettingKey: "lmstudio", systemOneModelSettingKey: "general", adblockBackendSettingKey: "jev"}, "jev", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, store := newClassifyRegistrationTestServer(t, config.Provider{}, nil)
			if err := store.SaveSettings(tc.settings); err != nil {
				t.Fatal(err)
			}
			backend, model := server.adblockSelection()
			if backend != tc.wantBackend || model != tc.wantModel {
				t.Fatalf("selection = %s/%s, want %s/%s", backend, model, tc.wantBackend, tc.wantModel)
			}
		})
	}
}

func TestAdblockConfigReportsClassifierToTheExtensionOnly(t *testing.T) {
	server, store := newClassifyRegistrationTestServer(t, config.Provider{}, nil)
	if err := store.SaveSettings(map[string]string{adblockBackendSettingKey: "lmstudio", adblockModelSettingKey: "local/model"}); err != nil {
		t.Fatal(err)
	}
	get := func(origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/browser-extension/adblock/config", nil)
		request.Header.Set("Origin", origin)
		recorder := httptest.NewRecorder()
		server.handleAdblockConfig(recorder, request)
		return recorder
	}
	if got := get("https://evil.example").Code; got != http.StatusForbidden {
		t.Fatalf("website status = %d", got)
	}
	recorder := get("chrome-extension://abc")
	if !strings.Contains(recorder.Body.String(), `"backend":"lmstudio"`) || !strings.Contains(recorder.Body.String(), `"model":"local/model"`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}
