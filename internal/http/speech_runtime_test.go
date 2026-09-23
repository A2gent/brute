package http

import (
	"context"
	"encoding/json"
	"github.com/A2gent/brute/internal/speechengine"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeSpeechInstaller struct {
	calls     int
	component string
	err       error
	job       *speechengine.InstallJob
}

func (f *fakeSpeechInstaller) Start(component string) (speechengine.InstallJob, error) {
	f.calls++
	f.component = component
	return speechengine.InstallJob{ID: "test", Component: component, State: "running"}, f.err
}
func (f *fakeSpeechInstaller) Job() *speechengine.InstallJob { return f.job }

func TestSpeechRuntimeInstallValidation(t *testing.T) {
	for _, tc := range []struct {
		body, contentType string
		want              int
	}{
		{`{"component":"mlx"}`, "application/json", 202},
		{`{"component":"ffmpeg"}`, "application/json", 202},
		{`{"component":"mlx; rm -rf /"}`, "application/json", 400},
		{`{"component":"mlx","command":"pip install evil"}`, "application/json", 400},
		{`{"component":"mlx"}{}`, "application/json", 400},
		{`{"component":"mlx"}`, "text/plain", 415},
		{`{}`, "application/json", 400},
	} {
		t.Run(tc.body+tc.contentType, func(t *testing.T) {
			installer := &fakeSpeechInstaller{}
			s := &Server{speechRuntimeInstaller: installer}
			req := httptest.NewRequest("POST", "/speech/runtime/install", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()
			s.handleInstallSpeechRuntime(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if tc.want != 202 && installer.calls != 0 {
				t.Fatal("invalid request started install")
			}
		})
	}
}

func TestSpeechRuntimeInstallConflict(t *testing.T) {
	s := &Server{speechRuntimeInstaller: &fakeSpeechInstaller{err: speechengine.ErrInstallBusy}}
	req := httptest.NewRequest("POST", "/speech/runtime/install", strings.NewReader(`{"component":"mlx"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleInstallSpeechRuntime(rec, req)
	if rec.Code != 409 {
		t.Fatal(rec.Code)
	}
}

func TestSpeechRuntimeRoutesAndJob(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	installer := &fakeSpeechInstaller{job: &speechengine.InstallJob{ID: "1", State: "running"}}
	s := &Server{speechRuntimeInstaller: installer, inspectSpeechRuntime: func(context.Context) speechengine.RuntimeStatus {
		return speechengine.RuntimeStatus{OS: "darwin", Arch: "arm64", Engines: []speechengine.EngineStatus{{ID: "parakeet"}}}
	}}
	s.setupRoutes()
	for _, url := range []string{"/speech/runtime", "/speech/runtime/install"} {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", url, rec.Code)
		}
		var result struct {
			Job *speechengine.InstallJob `json:"job"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Job == nil || result.Job.ID != "1" {
			t.Fatal("missing job")
		}
	}
}
