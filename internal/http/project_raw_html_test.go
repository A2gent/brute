package http

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestRawHTMLPreviewIsSandboxed(t *testing.T) {
	for _, name := range []string{"page.html", "page.htm", "page.HTML", "page.HTM"} {
		t.Run(name, func(t *testing.T) {
			server, projectID, projectDir := newProjectFileTestServer(t)
			content := "<!doctype html><html><body>Preview<script>document.body.dataset.live = 'yes'</script></body></html>"
			if err := os.WriteFile(filepath.Join(projectDir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			// The shared raw allowlist also enables HTML in the Mind vault.
			// Both handlers must sandbox direct navigation, not just iframe embedding.
			if err := server.store.SaveSettings(map[string]string{mindRootFolderSettingKey: projectDir}); err != nil {
				t.Fatal(err)
			}
			for _, target := range []string{
				"/projects/file/raw?projectID=" + url.QueryEscape(projectID) + "&path=" + url.QueryEscape(name),
				"/mind/file/raw?path=" + url.QueryEscape(name),
			} {
				t.Run(target, func(t *testing.T) {
					rec := httptest.NewRecorder()
					server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
					if rec.Code != http.StatusOK {
						t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
					}
					if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
						t.Fatalf("unexpected HTML content type: %q", got)
					}
					if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox allow-scripts" {
						t.Fatalf("expected scripts-only sandbox CSP, got %q", got)
					}
					if got := rec.Body.String(); got != content {
						t.Fatalf("expected unmodified HTML, got %q", got)
					}
				})
			}
		})
	}
}

func TestProjectRawPreviewRejectsHTMLTemplate(t *testing.T) {
	if isProjectRawPreviewFile("page.html.erb") {
		t.Fatal("HTML templates must not be served as raw previews")
	}
}
