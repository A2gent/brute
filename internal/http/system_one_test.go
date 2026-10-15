package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/jev"
)

func TestSystemOneLMStudioBackendWorksWithoutJevKey(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	var gotModel string
	lmstudio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"ok": {"yes": 0.8, "no": 0.2}}`}}},
		})
	}))
	defer lmstudio.Close()
	t.Setenv("LM_STUDIO_BASE_URL", lmstudio.URL+"/v1")

	server, store := newClassifyRegistrationTestServer(t, config.Provider{}, nil)
	if client, _ := server.systemOneClient(); client != nil {
		t.Fatal("default Jev backend without a key must stay unconfigured")
	}
	if err := store.SaveSettings(map[string]string{systemOneBackendSettingKey: "lmstudio", systemOneModelSettingKey: "local/model"}); err != nil {
		t.Fatal(err)
	}
	client, egressKey := server.systemOneClient()
	if client == nil || egressKey != "" {
		t.Fatalf("lmstudio backend not selected: client=%v key=%q", client, egressKey)
	}
	resp, err := client.SystemOne(context.Background(), jev.SystemOneRequest{
		State: "hi", Questions: map[string]jev.Question{"ok": {Type: "noul", Instructions: "greeting?"}},
	})
	if err != nil || resp.Answers["ok"].Noul == nil || *resp.Answers["ok"].Noul != 0.8 || gotModel != "local/model" {
		t.Fatalf("SystemOne via LM Studio: resp=%+v err=%v model=%q", resp, err, gotModel)
	}
	if server.relevanceGate() == nil {
		t.Fatal("automation must use the local backend")
	}
}
