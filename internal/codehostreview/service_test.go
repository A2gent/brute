package codehostreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestParseBitbucketRepository(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		remote    string
		workspace string
		repo      string
	}{
		{"git@bitbucket.org:acme/widgets.git", "acme", "widgets"},
		{"ssh://git@bitbucket.org/acme/widgets.git", "acme", "widgets"},
		{"https://bitbucket.org/acme/widgets.git", "acme", "widgets"},
	} {
		got, err := ParseRepositoryRemote(test.remote)
		if err != nil {
			t.Fatalf("ParseRepositoryRemote(%q): %v", test.remote, err)
		}
		if got.Provider != "bitbucket" || got.Owner != test.workspace || got.Name != test.repo {
			t.Fatalf("ParseRepositoryRemote(%q) = %#v", test.remote, got)
		}
	}
}

func TestServiceGetsBitbucketReviewAndNormalizesReplies(t *testing.T) {
	var gotAuthUser, gotAuthToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthUser, gotAuthToken, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/2.0/repositories/acme/widgets/pullrequests":
			if !strings.Contains(r.URL.Query().Get("q"), `source.branch.name="feature/review"`) {
				t.Fatalf("unexpected PR query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"values":[{"id":42,"title":"Reusable reviews","state":"OPEN","source":{"branch":{"name":"feature/review"}},"destination":{"branch":{"name":"main"}},"links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/42"}}}]}`))
		case "/2.0/repositories/acme/widgets/pullrequests/42/comments":
			_, _ = w.Write([]byte(`{"values":[
				{"id":7,"content":{"raw":"Please cover this case"},"user":{"display_name":"Reviewer","links":{"avatar":{"href":"https://img/reviewer"}}},"created_on":"2026-07-14T07:00:00Z","updated_on":"2026-07-14T07:00:00Z","inline":{"path":"src/app.ts","to":12},"links":{"html":{"href":"https://bitbucket/comment/7"}}},
				{"id":8,"parent":{"id":7},"content":{"raw":"Fixed in the latest commit"},"user":{"display_name":"Author"},"created_on":"2026-07-14T08:00:00Z","updated_on":"2026-07-14T08:00:00Z"}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := newReviewTestStore(t, server.URL)
	service := NewService(store, server.Client())
	result, err := service.GetReview(context.Background(), GetReviewRequest{
		Repository: Repository{Provider: "bitbucket", Owner: "acme", Name: "widgets"},
		Branch:     "feature/review",
	})
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if gotAuthUser != "dev@example.com" || gotAuthToken != "secret" {
		t.Fatalf("unexpected basic auth %q/%q", gotAuthUser, gotAuthToken)
	}
	if result.PullRequest == nil || result.PullRequest.ID != "42" || len(result.Comments) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := result.Comments[0]; got.FilePath != "src/app.ts" || got.LineNumber != 12 || got.Side != "additions" {
		t.Fatalf("unexpected inline comment: %#v", got)
	}
	if result.Comments[1].ParentID != "7" {
		t.Fatalf("expected reply parent, got %#v", result.Comments[1])
	}
}

func TestServiceRepliesToBitbucketComment(t *testing.T) {
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/2.0/repositories/acme/widgets/pullrequests/42/comments" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":9,"parent":{"id":7},"content":{"raw":"Done"},"user":{"display_name":"Author"},"created_on":"2026-07-14T09:00:00Z"}`))
	}))
	defer server.Close()

	service := NewService(newReviewTestStore(t, server.URL), server.Client())
	comment, err := service.Reply(context.Background(), ReplyRequest{
		Repository:    Repository{Provider: "bitbucket", Owner: "acme", Name: "widgets"},
		PullRequestID: "42",
		CommentID:     "7",
		Body:          "Done",
	})
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	content := body["content"].(map[string]interface{})
	parent := body["parent"].(map[string]interface{})
	if content["raw"] != "Done" || parent["id"] != float64(7) || comment.ParentID != "7" {
		t.Fatalf("unexpected reply payload/result: %#v %#v", body, comment)
	}
}

func TestServiceSelectsWorkspaceScopedIntegration(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	for _, integration := range []*storage.Integration{
		{ID: "one", Provider: "bitbucket", Name: "One", Mode: "duplex", Enabled: true, Config: map[string]string{"email": "one@example.com", "api_token": "one", "workspace": "one"}, CreatedAt: now, UpdatedAt: now},
		{ID: "acme", Provider: "bitbucket", Name: "Acme", Mode: "duplex", Enabled: true, Config: map[string]string{"email": "acme@example.com", "api_token": "acme", "workspace": "acme"}, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.SaveIntegration(integration); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(store, http.DefaultClient)
	credentials, err := service.resolveBitbucketCredentials(Repository{Provider: "bitbucket", Owner: "acme", Name: "widgets"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Email != "acme@example.com" {
		t.Fatalf("selected wrong integration: %#v", credentials)
	}
}

func newReviewTestStore(t *testing.T, baseURL string) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := url.Parse(baseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.SaveIntegration(&storage.Integration{
		ID: "bitbucket", Provider: "bitbucket", Name: "Bitbucket", Mode: "duplex", Enabled: true,
		Config:    map[string]string{"email": "dev@example.com", "api_token": "secret", "workspace": "acme", "api_base_url": baseURL},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return store
}
