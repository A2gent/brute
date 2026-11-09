package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlackProjectForChannel(t *testing.T) {
	server, store := newSlackRunServer(t, newFakeSlack(t), nil)
	saveSlackProject(t, store, "mapped-project")
	cases := []struct {
		name      string
		mapping   string
		fallback  string
		channel   string
		want      string
		wantAdmit bool
	}{
		{name: "mapped", mapping: `{"C1":"mapped-project"}`, fallback: slackTestProject, channel: "C1", want: "mapped-project", wantAdmit: true},
		{name: "unmapped uses default", mapping: `{"C1":"mapped-project"}`, fallback: slackTestProject, channel: "C2", want: slackTestProject, wantAdmit: true},
		{name: "no mapping uses default", fallback: slackTestProject, channel: "C2", want: slackTestProject, wantAdmit: true},
		{name: "unmapped without default", mapping: `{"C1":"mapped-project"}`, channel: "C2"},
		{name: "mapped project deleted", mapping: `{"C1":"gone"}`, fallback: slackTestProject, channel: "C1"},
		{name: "corrupt mapping", mapping: `[1]`, fallback: slackTestProject, channel: "C1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			integration := saveSlackIntegration(t, store, map[string]string{slackChannelProjectsKey: tc.mapping})
			integration.ProjectID = tc.fallback
			got, ok := server.slackProjectFor(integration, tc.channel)
			if got != tc.want || ok != tc.wantAdmit {
				t.Fatalf("slackProjectFor = %q, %v; want %q, %v", got, ok, tc.want, tc.wantAdmit)
			}
		})
	}
}

func TestSlackMentionRunsInChannelProject(t *testing.T) {
	fake := newFakeSlack(t)
	model := &fakeModel{}
	server, store := newSlackRunServer(t, fake, model)
	saveSlackProject(t, store, "mapped-project")
	saveSlackIntegration(t, store, map[string]string{"allowed_user_ids": "U1", slackChannelProjectsKey: `{"` + slackTestChannel + `":"mapped-project"}`})
	fake.setThread(slackThreadMsg(slackTestRoot, "U1", "<@UBOT> hi"))

	runSlackMention(server, slackMention(slackTestRoot, "", "U1", "<@UBOT> hi"))

	sessions, err := server.sessionManager.List()
	if err != nil || len(sessions) != 1 || sessions[0].ProjectID == nil || *sessions[0].ProjectID != "mapped-project" {
		t.Fatalf("sessions = %+v, err = %v", sessions, err)
	}
}

func TestSlackUnmappedChannelWithoutDefaultIsDenied(t *testing.T) {
	fake := newFakeSlack(t)
	model := &fakeModel{}
	server, store := newSlackRunServer(t, fake, model)
	integration := saveSlackIntegration(t, store, map[string]string{"allowed_user_ids": "U1", slackChannelProjectsKey: `{"C999":"` + slackTestProject + `"}`})
	integration.ProjectID = ""
	if err := store.SaveIntegration(integration); err != nil {
		t.Fatal(err)
	}

	runSlackMention(server, slackMention(slackTestRoot, "", "U1", "<@UBOT> hi"))

	calls := fake.callsTo("chat.postEphemeral")
	if len(calls) != 1 || calls[0].form.Get("text") != slackNoChannelMessage {
		t.Fatalf("ephemeral calls = %+v", calls)
	}
	if sessions, err := server.sessionManager.List(); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %d, err = %v", len(sessions), err)
	}
}

func TestSlackChannelProjectsValidation(t *testing.T) {
	server, store := newSlackRunServer(t, newFakeSlack(t), nil)
	saveSlackProject(t, store, slackTestProject)
	send := func(projectField, mapping string) *httptest.ResponseRecorder {
		body := `{"provider":"slack","mode":"duplex",` + projectField + `"config":{"bot_token":"` + slackTestBotToken + `","app_token":"` + slackTestAppToken + `","channel_projects":` + mapping + `}}`
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodPost, "/integrations", strings.NewReader(body)))
		return rec
	}
	quote := func(raw string) string {
		encoded, _ := json.Marshal(raw)
		return string(encoded)
	}

	for name, tc := range map[string]struct {
		project, mapping, wantErr string
	}{
		"bad json":         {mapping: `{"C1":`, wantErr: "channel_projects must be a JSON object"},
		"not an object":    {mapping: `["C1"]`, wantErr: "channel_projects must be a JSON object"},
		"unknown project":  {mapping: `{"C1":"missing"}`, wantErr: "project not found: missing"},
		"nothing to run":   {mapping: `{"C1":""}`, wantErr: "default project or at least one channel mapping"},
		"default required": {mapping: ``, wantErr: "default project or at least one channel mapping"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := send(tc.project, quote(tc.mapping))
			if rec.Code != nethttp.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}

	rec := send("", quote(` {" C1 ":" `+slackTestProject+` ","C2":""} `))
	if rec.Code != nethttp.StatusCreated {
		t.Fatalf("mapping only = %d %s", rec.Code, rec.Body)
	}
	var created IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if got := created.Config[slackChannelProjectsKey]; got != `{"C1":"`+slackTestProject+`"}` {
		t.Fatalf("normalized mapping = %q", got)
	}

	rec = send(`"project_id":"`+slackTestProject+`",`, quote(`{"C1":""}`))
	if rec.Code != nethttp.StatusCreated {
		t.Fatalf("default only = %d %s", rec.Code, rec.Body)
	}
	var defaultOnly IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &defaultOnly); err != nil {
		t.Fatal(err)
	}
	if _, ok := defaultOnly.Config[slackChannelProjectsKey]; ok {
		t.Fatalf("empty mapping kept: %v", defaultOnly.Config)
	}
}

func postSlackChannels(server *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodPost, "/integrations/slack/channels", strings.NewReader(body)))
	return rec
}

func TestSlackChannelsEndpoint(t *testing.T) {
	fake := newFakeSlack(t)
	fake.channels = [][]slackConversation{
		{{ID: "C1", Name: "general"}},
		{{ID: "G1", Name: "secret", IsPrivate: true}},
	}
	server, store := newSlackRunServer(t, fake, nil)
	saveSlackIntegration(t, store, nil)

	for _, body := range []string{`{"bot_token":"xoxb-draft"}`, `{"bot_token":"***","integration_id":"slack-1"}`} {
		rec := postSlackChannels(server, body)
		if rec.Code != nethttp.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", body, rec.Code, rec.Body.String())
		}
		var response SlackChannelsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		want := []SlackChannelResponse{{ID: "C1", Name: "general"}, {ID: "G1", Name: "secret", IsPrivate: true}}
		if len(response.Channels) != 2 || response.Channels[0] != want[0] || response.Channels[1] != want[1] {
			t.Fatalf("%s: channels = %+v", body, response.Channels)
		}
	}
	calls := fake.callsTo("users.conversations")
	if calls[0].token != "xoxb-draft" || calls[len(calls)-1].token != slackTestBotToken {
		t.Fatalf("masked token was not resolved: first=%q last=%q", calls[0].token, calls[len(calls)-1].token)
	}
	if form := calls[0].form; form.Get("types") != "public_channel,private_channel" || form.Get("exclude_archived") != "true" {
		t.Fatalf("form = %v", form)
	}

	if rec := postSlackChannels(server, `{}`); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("missing token status = %d", rec.Code)
	}
	fake.methodErrs["users.conversations"] = "missing_scope"
	rec := postSlackChannels(server, `{"bot_token":"xoxb-draft"}`)
	if rec.Code != nethttp.StatusBadGateway || !strings.Contains(rec.Body.String(), "missing_scope") || !strings.Contains(rec.Body.String(), "channels:read") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
