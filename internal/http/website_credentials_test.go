package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/websitecredentials"
)

func newWebsiteCredentialsTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	store, err := storage.NewSQLiteStore(cfg.DataPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), session.NewManager(store), store, speechcache.New(0), 0)
}

// localCredentialRequest mimics live Caesar on Vite :1420 calling the local API.
func localCredentialRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "localhost:5445"
	req.Header.Set("Origin", "http://localhost:1420")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func credentialRequest(t *testing.T, s *Server, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, localCredentialRequest(method, path, body))
	if rec.Code != status {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, rec.Code, status, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credentials response can be cached")
	}
	return rec
}

func TestWebsiteCredentialsCRUD(t *testing.T) {
	s := newWebsiteCredentialsTestServer(t)
	var accessLog bytes.Buffer
	s.EnableHTTPAccessLog(&accessLog)
	rec := credentialRequest(t, s, "GET", "/secrets", "", 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatal("fresh list not empty array")
	}
	if _, err := os.Stat(filepath.Join(s.config.DataPath, "website-credentials")); !os.IsNotExist(err) {
		t.Fatal("fresh list created credential files")
	}
	rec = credentialRequest(t, s, "POST", "/secrets", `{"url":"https://example.com/login","login":"alice","password":" secret-password\n "}`, 201)
	var created websitecredentials.Metadata
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || !created.HasPassword || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("missing metadata: %v", created)
	}
	for _, response := range []string{rec.Body.String(), credentialRequest(t, s, "GET", "/secrets", "", 200).Body.String()} {
		if strings.Contains(response, `"password"`) || strings.Contains(response, "secret-password") {
			t.Fatal("CRUD returned password")
		}
	}
	for _, body := range []string{`{"url":"https://example.com/new","login":"bob"}`, `{"url":"https://example.com/new","login":"bob","password":""}`} {
		rec = credentialRequest(t, s, "PUT", "/secrets/"+created.ID, body, 200)
		if strings.Contains(rec.Body.String(), `"password"`) {
			t.Fatal("update returned password")
		}
		got, err := s.websiteCredentials.Get(created.ID)
		if err != nil || got.Password != " secret-password\n " || got.Login != "bob" {
			t.Fatal("update did not preserve password", err)
		}
	}
	rec = credentialRequest(t, s, "DELETE", "/secrets/"+created.ID, "", 204)
	if rec.Body.Len() != 0 {
		t.Fatal("delete returned body")
	}
	credentialRequest(t, s, "DELETE", "/secrets/"+created.ID, "", 404)
	credentialRequest(t, s, "PUT", "/secrets/missing", `{"url":"https://example.com","login":"bob"}`, 404)
	if strings.Contains(accessLog.String(), "secret-password") {
		t.Fatal("request logger leaked password")
	}
}

func TestWebsiteCredentialsInvalidRequests(t *testing.T) {
	s := newWebsiteCredentialsTestServer(t)
	for _, body := range []string{
		`{"url":"file:///tmp/test","login":"alice","password":"secret-password"}`,
		`{"url":"https://alice:secret-password@example.com","login":"alice","password":"secret-password"}`,
		`{"url":"https://example.com","password":"secret-password"}`,
		`{"url":"https://example.com","login":"alice"}`,
		`{"url":"https://example.com","login":"alice","password":123}`,
		`{"password":"secret-password"} {}`,
		`{"password":"secret-password"`,
		`null`,
	} {
		rec := credentialRequest(t, s, "POST", "/secrets", body, 400)
		if strings.Contains(rec.Body.String(), "secret-password") {
			t.Fatal("error echoed input")
		}
	}
	body := `{"url":"https://example.com","login":"alice","password":"` + strings.Repeat("x", websiteCredentialsMaxBody) + `"}`
	credentialRequest(t, s, "POST", "/secrets", body, 413)
}

func TestWebsiteCredentialsCorruptionResponse(t *testing.T) {
	s := newWebsiteCredentialsTestServer(t)
	_, err := s.websiteCredentials.Create("https://example.com", "alice", "secret-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.config.DataPath, "website-credentials", "records.enc"), []byte("secret-corrupt-ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, req := range [][3]string{{"GET", "/secrets", ""}, {"POST", "/secrets", `{"url":"https://example.com","login":"alice","password":"secret-password"}`}, {"DELETE", "/secrets/missing", ""}} {
		rec := credentialRequest(t, s, req[0], req[1], req[2], 500)
		if strings.Contains(rec.Body.String(), "ciphertext") || strings.Contains(rec.Body.String(), "secret-password") {
			t.Fatal("error leaked secret data")
		}
	}
}

func TestWebsiteCredentialsToolAndRegistration(t *testing.T) {
	s := newWebsiteCredentialsTestServer(t)
	created, err := s.websiteCredentials.Create("https://example.com", "alice", "secret-password")
	if err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	projectID := "credentials-project"
	if err := s.store.SaveProject(&storage.Project{ID: projectID, Name: "Credentials", Folder: &projectDir, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sess := &session.Session{ID: "credential-session", ProjectID: &projectID}
	for _, manager := range []*tools.Manager{s.toolManager, s.toolManagerForSession(nil), s.toolManagerForSession(sess), s.buildSubAgentToolManager(sess, nil)} {
		tool, ok := manager.Get("website_credentials")
		if !ok {
			t.Fatal("tool missing from manager")
		}
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"list"}`))
		if err != nil || !result.Success || !strings.Contains(result.Output, created.ID) || strings.Contains(result.Output, `"password"`) || strings.Contains(result.Output, "secret-password") {
			t.Fatalf("tool list: %v %v", result, err)
		}
		params, _ := json.Marshal(map[string]string{"operation": "get", "id": created.ID})
		result, err = tool.Execute(context.Background(), params)
		if err != nil || !result.Success {
			t.Fatalf("tool get: %v %v", result, err)
		}
		var output map[string]string
		if err := json.Unmarshal([]byte(result.Output), &output); err != nil {
			t.Fatal(err)
		}
		if len(output) != 4 || output["password"] != "secret-password" || output["url"] != "https://example.com" {
			t.Fatalf("get shape: %v", output)
		}
		for _, input := range []string{`{`, `{"operation":"get"}`, `{"operation":"get","id":"missing"}`, `{"operation":"delete","id":"anything"}`} {
			result, err := tool.Execute(context.Background(), json.RawMessage(input))
			if err != nil || result.Success || strings.Contains(result.Error, "secret-password") {
				t.Fatalf("invalid tool call: %v %v", result, err)
			}
		}
	}
	found := false
	for _, def := range s.mcpBridgeToolList(sess) {
		if def["name"] == "website_credentials" {
			found = true
		}
	}
	if !found {
		t.Fatal("tool not exposed through MCP")
	}
	// The normal disabled-tool policy must still apply to credential access.
	if err := s.store.SaveSettings(map[string]string{disabledToolsSettingKey: `["website_credentials"]`}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.toolManagerForSession(sess).Get("website_credentials"); ok {
		t.Fatal("disabled tool still available")
	}
}

func TestWebsiteCredentialsLocalOnlyGuard(t *testing.T) {
	s := newWebsiteCredentialsTestServer(t)
	s.config.CORSAllowedOrigins = []string{"*", "http://localhost:3000", "https://*.evil.example", "null"}
	vault := filepath.Join(s.config.DataPath, "website-credentials")
	createBody := `{"url":"https://example.com","login":"alice","password":"secret-password"}`
	send := func(method, path, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		req := localCredentialRequest(method, path, body)
		edit(req)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s response can be cached", method, path)
		}
		return rec
	}
	setOrigin := func(origin string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Origin", origin) }
	}
	denied := map[string]func(*http.Request){
		"remote peer":      func(r *http.Request) { r.RemoteAddr = "192.0.2.10:4000" },
		"remote ipv6 peer": func(r *http.Request) { r.RemoteAddr = "[2001:db8::1]:4000" },
		"forged forwarded for": func(r *http.Request) {
			r.RemoteAddr = "192.0.2.10:4000"
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Real-IP", "127.0.0.1")
			r.Header.Set("Forwarded", "for=127.0.0.1")
		},
		"rebinding host": func(r *http.Request) {
			r.Host = "attacker.example:5445"
			r.Header.Set("Origin", "http://attacker.example:5445")
		},
		"rebinding host no origin": func(r *http.Request) {
			r.Host = "attacker.example:5445"
			r.Header.Del("Origin")
		},
		"forwarded host":          func(r *http.Request) { r.Host = "attacker.example"; r.Header.Set("X-Forwarded-Host", "localhost:5445") },
		"untrusted origin":        setOrigin("https://evil.example"),
		"wildcard pattern":        setOrigin("https://a.evil.example"),
		"null origin":             setOrigin("null"),
		"loopback other port":     setOrigin("http://localhost:8080"),
		"non-loopback port 1420":  setOrigin("http://evil.example:1420"),
		"own origin wrong scheme": setOrigin("https://localhost:5445"),
		"cross-site no origin": func(r *http.Request) {
			r.Header.Del("Origin")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		},
	}
	for name, edit := range denied {
		for _, req := range [][3]string{{"GET", "/secrets", ""}, {"POST", "/secrets", createBody}, {"PUT", "/secrets/x", createBody}, {"DELETE", "/secrets/x", ""}} {
			if rec := send(req[0], req[1], req[2], edit); rec.Code != http.StatusForbidden {
				t.Fatalf("%s: %s %s allowed: %d", name, req[0], req[1], rec.Code)
			}
		}
	}
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		if rec := send("POST", "/secrets", createBody, func(r *http.Request) { r.Header.Set("Content-Type", contentType) }); rec.Code != http.StatusForbidden {
			t.Fatalf("simple %q mutation allowed: %d", contentType, rec.Code)
		}
	}
	if _, err := os.Stat(vault); !os.IsNotExist(err) {
		t.Fatal("denied request opened the vault")
	}

	allowed := map[string]func(*http.Request){
		"vite localhost":     func(*http.Request) {},
		"vite 127.0.0.1":     setOrigin("http://127.0.0.1:1420"),
		"vite ipv6":          setOrigin("http://[::1]:1420"),
		"tauri scheme":       setOrigin("tauri://localhost"),
		"tauri http":         setOrigin("http://tauri.localhost"),
		"tauri https":        setOrigin("https://tauri.localhost"),
		"same origin":        setOrigin("http://localhost:5445"),
		"configured origin":  setOrigin("http://localhost:3000"),
		"cli no origin":      func(r *http.Request) { r.Header.Del("Origin") },
		"ipv6 peer and host": func(r *http.Request) { r.RemoteAddr = "[::1]:50000"; r.Host = "[::1]:5445"; r.Header.Del("Origin") },
		"json charset": func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
		},
	}
	for name, edit := range allowed {
		rec := send("POST", "/secrets", createBody, edit)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: create denied: %d %s", name, rec.Code, rec.Body.String())
		}
		var created websitecredentials.Metadata
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if rec := send("GET", "/secrets", "", edit); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
			t.Fatalf("%s: list: %d", name, rec.Code)
		}
		if rec := send("PUT", "/secrets/"+created.ID, `{"url":"https://example.com","login":"bob"}`, edit); rec.Code != http.StatusOK {
			t.Fatalf("%s: update: %d", name, rec.Code)
		}
		if rec := send("DELETE", "/secrets/"+created.ID, "", edit); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: delete: %d", name, rec.Code)
		}
	}
}
