package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateJobKeepsProvidedScheduleCron(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	body := `{"name":"Moved","schedule_text":"every day at 10:30","schedule_cron":"30 10 * * *","task_prompt":"do it","enabled":true}`
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body)))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	var created JobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ScheduleCron != "30 10 * * *" || created.ScheduleHuman != "every day at 10:30" {
		t.Fatalf("created schedule = %q / %q", created.ScheduleCron, created.ScheduleHuman)
	}

	bad := `{"name":"Moved","schedule_text":"x","schedule_cron":"not a cron","task_prompt":"do it"}`
	rec = httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(bad)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cron status = %d, want 400", rec.Code)
	}
}
