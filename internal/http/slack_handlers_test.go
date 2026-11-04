package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/storage"
)

func TestSlackValidation(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		config  map[string]string
		wantErr string
	}{
		{name: "valid duplex", mode: "duplex", config: map[string]string{"bot_token": "xoxb-1", "app_token": "xapp-1"}},
		{name: "valid workspace", mode: "duplex", config: map[string]string{"bot_token": "xoxb-1", "app_token": "xapp-1", "access_mode": "workspace"}},
		{name: "channel not needed for duplex", mode: "duplex", config: map[string]string{"bot_token": "xoxb-1", "app_token": "xapp-1", "channel_id": ""}},
		{name: "legacy notify_only", mode: "notify_only", config: map[string]string{"bot_token": "xoxb-1", "channel_id": "C1"}},
		{name: "bot token required", mode: "duplex", config: map[string]string{"app_token": "xapp-1"}, wantErr: "missing required config field: bot_token"},
		{name: "duplex needs app token", mode: "duplex", config: map[string]string{"bot_token": "xoxb-1"}, wantErr: "missing required config field: app_token"},
		{name: "notify_only needs channel", mode: "notify_only", config: map[string]string{"bot_token": "xoxb-1"}, wantErr: "channel_id"},
		{name: "swapped bot token", mode: "duplex", config: map[string]string{"bot_token": "xapp-1", "app_token": "xapp-2"}, wantErr: "looks like an app-level token"},
		{name: "swapped app token", mode: "duplex", config: map[string]string{"bot_token": "xoxb-1", "app_token": "xoxb-2"}, wantErr: "looks like a bot token"},
		{name: "wrong bot prefix", mode: "duplex", config: map[string]string{"bot_token": "abc", "app_token": "xapp-1"}, wantErr: "bot_token must start with xoxb-"},
		{name: "wrong app prefix", mode: "duplex", config: map[string]string{"bot_token": "xoxb-1", "app_token": "abc"}, wantErr: "app_token must start with xapp-"},
		{name: "bad access mode", mode: "duplex", config: map[string]string{"bot_token": "xoxb-1", "app_token": "xapp-1", "access_mode": "public"}, wantErr: "access_mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newIntegrationFromRequest(IntegrationRequest{Provider: "slack", Mode: tc.mode, Config: tc.config})
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestSlackConfigNormalizationAndMasking(t *testing.T) {
	integration, err := newIntegrationFromRequest(IntegrationRequest{Provider: "slack", Mode: "duplex", Config: map[string]string{"bot_token": "xoxb-1", "app_token": "xapp-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if integration.Config["access_mode"] != "allowlist" {
		t.Fatalf("access_mode = %q", integration.Config["access_mode"])
	}
	response := integrationToResponse(integration)
	if response.Config["app_token"] != "***" || response.Config["bot_token"] != "***" {
		t.Fatalf("secrets not masked: %v", response.Config)
	}
}

func TestSlackTokensBlockJevEgress(t *testing.T) {
	for _, text := range []string{"xoxb-1234567890-abcdef", "xapp-1-A123-456-abcdef"} {
		if jev.SafeEgressText("see "+text, nil) {
			t.Fatalf("%q must not leave through Jev", text)
		}
	}
}

func TestSlackIntegrationCannotBeSavedWithoutProject(t *testing.T) {
	server, store := newSlackRunServer(t, newFakeSlack(t), nil)
	saveSlackProject(t, store, slackTestProject)
	send := func(method, path, projectField string) *httptest.ResponseRecorder {
		body := `{"provider":"slack","mode":"duplex",` + projectField + `"config":{"bot_token":"` + slackTestBotToken + `","app_token":"` + slackTestAppToken + `","access_mode":"workspace"}}`
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	if rec := send(nethttp.MethodPost, "/integrations", ""); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("create without project = %d %s", rec.Code, rec.Body)
	}
	rec := send(nethttp.MethodPost, "/integrations", `"project_id":"`+slackTestProject+`",`)
	if rec.Code != nethttp.StatusCreated && rec.Code != nethttp.StatusOK {
		t.Fatalf("create with project = %d %s", rec.Code, rec.Body)
	}
	var created IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if rec := send(nethttp.MethodPut, "/integrations/"+created.ID, `"project_id":"",`); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("clearing project = %d %s", rec.Code, rec.Body)
	}
}

func postSlackDraftTest(t *testing.T, server *Server) (int, IntegrationTestResponse) {
	t.Helper()
	body := `{"provider":"slack","mode":"duplex","config":{"bot_token":"` + slackTestBotToken + `","app_token":"` + slackTestAppToken + `"}}`
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodPost, "/integrations/test", strings.NewReader(body)))
	var response IntegrationTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, response
}

func TestSlackIntegrationTestNamesTheFailedToken(t *testing.T) {
	cases := []struct {
		name       string
		errors     map[string]string
		wantStatus int
		wantParts  []string
	}{
		{name: "success", wantStatus: nethttp.StatusOK, wantParts: []string{"Connected to Acme as @agent"}},
		{name: "bot token", errors: map[string]string{"auth.test": "invalid_auth"}, wantStatus: nethttp.StatusBadGateway, wantParts: []string{"bot token", "invalid_auth", "xoxb-"}},
		{name: "app token scope", errors: map[string]string{"apps.connections.open": "missing_scope"}, wantStatus: nethttp.StatusBadGateway, wantParts: []string{"app token", "connections:write"}},
		{name: "app token rejected", errors: map[string]string{"apps.connections.open": "invalid_auth"}, wantStatus: nethttp.StatusBadGateway, wantParts: []string{"app token", "xapp-"}},
		{name: "unknown failure mentions socket mode", errors: map[string]string{"apps.connections.open": "forbidden_team"}, wantStatus: nethttp.StatusBadGateway, wantParts: []string{"app token", "Socket Mode", "connections:write"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeSlack(t)
			fake.methodErrs = map[string]string{}
			for method, code := range tc.errors {
				fake.methodErrs[method] = code
			}
			server, _ := newSlackRunServer(t, fake, nil)

			status, response := postSlackDraftTest(t, server)
			if status != tc.wantStatus || response.Success != (tc.wantStatus == nethttp.StatusOK) {
				t.Fatalf("status = %d, response = %+v", status, response)
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(response.Message, part) {
					t.Fatalf("message %q lacks %q", response.Message, part)
				}
			}
			for _, secret := range []string{slackTestBotToken, slackTestAppToken} {
				if strings.Contains(response.Message, secret) {
					t.Fatal("message leaked a token")
				}
			}
		})
	}
}

func postSlackUsers(server *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodPost, "/integrations/slack/users", strings.NewReader(body)))
	return rec
}

func TestSlackUsersEndpoint(t *testing.T) {
	fake := newFakeSlack(t)
	fake.members = [][]slackUser{
		{{ID: "U1", Name: "alice", RealName: "Alice A"}, {ID: "U2", Name: "gone", Deleted: true}},
		{{ID: "B1", Name: "helper", IsBot: true}, {ID: "USLACKBOT", Name: "slackbot"}, {ID: "U3", Name: "bob", RealName: "Bob B"}},
	}
	server, store := newSlackRunServer(t, fake, nil)
	saveSlackIntegration(t, store, nil)
	other := &storage.Integration{ID: "tg-1", Provider: "telegram", Name: "Bot", Mode: "duplex", Enabled: true, Config: map[string]string{"bot_token": "telegram-secret"}}
	if err := store.SaveIntegration(other); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		`{"bot_token":"xoxb-draft"}`,
		`{"bot_token":"***","integration_id":"slack-1"}`,
		`{"integration_id":"slack-1"}`,
	} {
		rec := postSlackUsers(server, body)
		if rec.Code != nethttp.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", body, rec.Code, rec.Body.String())
		}
		var response SlackUsersResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Users) != 2 || response.Users[0] != (SlackUserResponse{ID: "U1", Name: "alice", RealName: "Alice A"}) || response.Users[1].ID != "U3" {
			t.Fatalf("%s: users = %+v", body, response.Users)
		}
	}
	calls := fake.callsTo("users.list")
	if calls[0].token != "xoxb-draft" || calls[len(calls)-1].token != slackTestBotToken {
		t.Fatalf("masked token was not resolved: first=%q last=%q", calls[0].token, calls[len(calls)-1].token)
	}

	before := len(fake.callsTo("users.list"))
	for _, body := range []string{`{}`, `{"bot_token":"***"}`, `{"bot_token":"***","integration_id":"tg-1"}`, `{"integration_id":"missing"}`} {
		rec := postSlackUsers(server, body)
		if rec.Code != nethttp.StatusBadRequest || !strings.Contains(rec.Body.String(), "bot_token is required") {
			t.Fatalf("%s: status = %d, body = %s", body, rec.Code, rec.Body.String())
		}
	}
	if got := len(fake.callsTo("users.list")); got != before {
		t.Fatal("another provider's token was sent to Slack")
	}

	fake.methodErrs["users.list"] = "invalid_auth"
	if rec := postSlackUsers(server, `{"bot_token":"xoxb-bad"}`); rec.Code != nethttp.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
}
