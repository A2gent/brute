package http

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestRawAbsGlbOutsideProject(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	outside := t.TempDir()
	glb := filepath.Join(outside, "model.glb")
	txt := filepath.Join(outside, "secret.txt")
	for path, data := range map[string]string{glb: "glTF", txt: "secret"} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	get := func(abs string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		target := "/projects/file/raw?projectID=" + url.QueryEscape(projectID) + "&absPath=" + url.QueryEscape(abs)
		server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}

	if rec := get(glb); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "model/gltf-binary" {
		t.Fatalf("expected glb served, got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := get(txt); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-glb must be rejected, got %d", rec.Code)
	}
	if rec := get("relative/model.glb"); rec.Code != http.StatusBadRequest {
		t.Fatalf("relative path must be rejected, got %d", rec.Code)
	}
}
