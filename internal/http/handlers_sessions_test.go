package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleListSessionsFiltersProjectAndMetadataKeys(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)

	projectID := "project-a"
	otherProjectID := "project-b"
	keepSession, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create keep session: %v", err)
	}
	keepSession.ProjectID = &projectID
	keepSession.Metadata = map[string]interface{}{
		"keep": "yes",
		"drop": "no",
	}
	if err := server.sessionManager.Save(keepSession); err != nil {
		t.Fatalf("save keep session: %v", err)
	}

	otherSession, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	otherSession.ProjectID = &otherProjectID
	otherSession.Metadata = map[string]interface{}{"keep": "other"}
	if err := server.sessionManager.Save(otherSession); err != nil {
		t.Fatalf("save other session: %v", err)
	}

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/?include_metadata=true&project_id=project-a&metadata_keys=keep", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var items []SessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode sessions response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one filtered session, got %d: %#v", len(items), items)
	}
	if items[0].ID != keepSession.ID {
		t.Fatalf("expected session %q, got %q", keepSession.ID, items[0].ID)
	}
	if items[0].ProjectID != projectID {
		t.Fatalf("expected project %q, got %q", projectID, items[0].ProjectID)
	}
	if got := items[0].Metadata["keep"]; got != "yes" {
		t.Fatalf("expected kept metadata, got %#v", items[0].Metadata)
	}
	if _, ok := items[0].Metadata["drop"]; ok {
		t.Fatalf("expected drop metadata to be omitted, got %#v", items[0].Metadata)
	}
}

func TestHandleGetSessionFiltersMetadataKeys(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.Metadata = map[string]interface{}{
		"keep": "yes",
		"drop": "no",
	}
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/"+sess.ID+"?include_messages=false&include_metadata=true&metadata_keys=keep", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var item SessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if got := item.Metadata["keep"]; got != "yes" {
		t.Fatalf("expected kept metadata, got %#v", item.Metadata)
	}
	if _, ok := item.Metadata["drop"]; ok {
		t.Fatalf("expected drop metadata to be omitted, got %#v", item.Metadata)
	}
	if len(item.Messages) != 0 {
		t.Fatalf("expected messages to be omitted, got %#v", item.Messages)
	}
}
