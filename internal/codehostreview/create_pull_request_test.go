package codehostreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestServiceCreatesBitbucketPullRequest(t *testing.T) {
	for _, destination := range []string{"", "release/next"} {
		t.Run("destination="+destination, func(t *testing.T) {
			var created, fetchedMain, fetchedNext bool
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, token, _ := r.BasicAuth()
				if user != "dev@example.com" || token != "secret" {
					t.Error("missing integration credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /2.0/repositories/acme/widgets/pullrequests":
					if r.URL.Query().Get("state") != "OPEN" || r.URL.Query().Get("q") != `source.branch.name="feature/create"` {
						t.Errorf("unexpected lookup query: %s", r.URL.RawQuery)
					}
					_, _ = w.Write([]byte(`{"values":[]}`))
				case "GET /2.0/repositories/acme/widgets":
					fetchedMain = true
					_, _ = w.Write([]byte(`{"mainbranch":{"name":"trunk"}}`))
				case "GET /2.0/user":
					_, _ = w.Write([]byte(`{"uuid":"{author}","display_name":"Author"}`))
				case "GET /2.0/repositories/acme/widgets/effective-default-reviewers":
					if r.URL.Query().Get("page") == "2" {
						fetchedNext = true
						_, _ = w.Write([]byte(`{"values":[{"user":{"uuid":"{bob}","nickname":"bob"},"reviewer_type":"repository"}]}`))
					} else {
						_ = json.NewEncoder(w).Encode(map[string]interface{}{
							"values": []map[string]interface{}{{"user": map[string]string{"uuid": "{author}", "display_name": "Author"}}, {"user": map[string]string{"uuid": "{alice}", "display_name": "Alice"}, "reviewer_type": "project"}},
							"next":   server.URL + "/2.0/repositories/acme/widgets/effective-default-reviewers?page=2",
						})
					}
				case "POST /2.0/repositories/acme/widgets/pullrequests":
					created = true
					var body struct {
						Title       string `json:"title"`
						Description string `json:"description"`
						Source      struct {
							Branch struct{ Name string } `json:"branch"`
						} `json:"source"`
						Destination struct {
							Branch struct{ Name string } `json:"branch"`
						} `json:"destination"`
						CloseSourceBranch bool                `json:"close_source_branch"`
						Reviewers         []map[string]string `json:"reviewers"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					wantDestination := destination
					if wantDestination == "" {
						wantDestination = "trunk"
					}
					if body.Title != "Create natively" || body.Description != "## Tests\nAll passed." || body.Source.Branch.Name != "feature/create" || body.Destination.Branch.Name != wantDestination || !body.CloseSourceBranch {
						t.Errorf("unexpected create payload: %#v", body)
					}
					if !reflect.DeepEqual(body.Reviewers, []map[string]string{{"uuid": "{alice}"}, {"uuid": "{bob}"}}) {
						t.Errorf("reviewers must include all pages, UUIDs only, without the author: %#v", body.Reviewers)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":43,"title":"Create natively","state":"OPEN","source":{"branch":{"name":"feature/create"}},"destination":{"branch":{"name":"` + wantDestination + `"}},"links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/43"}},"reviewers":[{"uuid":"{alice}","display_name":"Alice"},{"uuid":"{bob}","nickname":"bob"}]}`))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			service := NewService(newReviewTestStore(t, server.URL), server.Client())
			pr, err := service.CreatePullRequest(context.Background(), CreatePullRequestRequest{
				Repository:   Repository{Provider: "bitbucket", Owner: "acme", Name: "widgets"},
				SourceBranch: "feature/create", Title: "Create natively", Description: "## Tests\nAll passed.", DestinationBranch: destination,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !created || !fetchedNext || fetchedMain != (destination == "") {
				t.Fatalf("create=%v nextPage=%v mainBranch=%v", created, fetchedNext, fetchedMain)
			}
			if pr.ID != "43" || pr.URL != "https://bitbucket.org/acme/widgets/pull-requests/43" || pr.SourceBranch != "feature/create" || !reflect.DeepEqual(pr.Reviewers, []string{"Alice", "bob"}) {
				t.Fatalf("unexpected result: %#v", pr)
			}
		})
	}
}

func TestServiceCreatePullRequestReturnsExisting(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/2.0/repositories/acme/widgets/pullrequests" {
			t.Errorf("existing PR must short-circuit creation: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"values":[{"id":42,"title":"Original title","state":"OPEN","source":{"branch":{"name":"feature/create"}},"destination":{"branch":{"name":"trunk"}},"links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/42"}},"reviewers":[{"display_name":"Alice"}]}]}`))
	}))
	defer server.Close()
	service := NewService(newReviewTestStore(t, server.URL), server.Client())
	for range 2 {
		pr, err := service.CreatePullRequest(context.Background(), CreatePullRequestRequest{
			Repository:   Repository{Provider: "bitbucket", Owner: "acme", Name: "widgets"},
			SourceBranch: "feature/create", Title: "Do not change this PR",
		})
		if err != nil {
			t.Fatal(err)
		}
		if pr.ID != "42" || pr.Title != "Original title" || pr.URL != "https://bitbucket.org/acme/widgets/pull-requests/42" || !reflect.DeepEqual(pr.Reviewers, []string{"Alice"}) {
			t.Fatalf("unexpected existing PR: %#v", pr)
		}
	}
	if requests != 2 {
		t.Fatalf("expected one lookup per call, got %d requests", requests)
	}
}

func TestServiceCreatePullRequestWithNoOtherReviewers(t *testing.T) {
	for _, defaults := range []string{`[]`, `[{"user":{"uuid":"{author}","display_name":"Author"}}]`} {
		t.Run(defaults, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost:
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if string(body["reviewers"]) != "[]" {
						t.Errorf("expected an empty reviewers array, got %s", body["reviewers"])
					}
					_, _ = w.Write([]byte(`{"id":43}`))
				case r.URL.Path == "/2.0/user":
					_, _ = w.Write([]byte(`{"uuid":"{author}"}`))
				case strings.HasSuffix(r.URL.Path, "/effective-default-reviewers"):
					_, _ = w.Write([]byte(`{"values":` + defaults + `}`))
				default:
					_, _ = w.Write([]byte(`{"values":[]}`))
				}
			}))
			defer server.Close()
			service := NewService(newReviewTestStore(t, server.URL), server.Client())
			pr, err := service.CreatePullRequest(context.Background(), CreatePullRequestRequest{
				Repository:   Repository{Provider: "bitbucket", Owner: "acme", Name: "widgets"},
				SourceBranch: "feature/create", Title: "No reviewers", DestinationBranch: "trunk",
			})
			if err != nil || pr.ID != "43" {
				t.Fatalf("pr=%#v err=%v", pr, err)
			}
		})
	}
}

func TestServiceCreatePullRequestStopsOnAPIErrors(t *testing.T) {
	for _, failure := range []string{
		"GET /2.0/repositories/acme/widgets/pullrequests",
		"GET /2.0/repositories/acme/widgets",
		"GET /2.0/user",
		"GET /2.0/repositories/acme/widgets/effective-default-reviewers",
		"POST /2.0/repositories/acme/widgets/pullrequests",
	} {
		t.Run(failure, func(t *testing.T) {
			failed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failed {
					t.Error("request made after API failure")
				}
				if r.Method+" "+r.URL.Path == failure {
					failed = true
					http.Error(w, "insufficient scope", http.StatusForbidden)
					return
				}
				_, _ = w.Write([]byte(`{"values":[],"mainbranch":{"name":"trunk"},"uuid":"{author}"}`))
			}))
			defer server.Close()
			service := NewService(newReviewTestStore(t, server.URL), server.Client())
			_, err := service.CreatePullRequest(context.Background(), CreatePullRequestRequest{
				Repository:   Repository{Provider: "bitbucket", Owner: "acme", Name: "widgets"},
				SourceBranch: "feature/create", Title: "API failure",
			})
			if !failed || err == nil || !strings.Contains(err.Error(), "403") {
				t.Fatalf("failed=%v err=%v", failed, err)
			}
		})
	}
}
