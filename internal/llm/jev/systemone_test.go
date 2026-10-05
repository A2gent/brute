package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestSystemOneRequestAndAnswers(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
		}
		var got SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got.State != "report" || got.Model != "jev-latest" || len(got.Questions) != 3 {
			t.Errorf("request = %+v", got)
		}
		if !reflect.DeepEqual(got.Questions["choice"].Criteria, map[string]any{"a": "code", "b": "docs"}) {
			t.Errorf("choice = %+v", got.Questions["choice"])
		}
		if !reflect.DeepEqual(got.Questions["score"].Criteria, []any{"low", "high"}) {
			t.Errorf("score = %+v", got.Questions["score"])
		}
		_, _ = w.Write([]byte(`{"answers":{"choice":{"type":"choice","choice":"a","confidence":0.9,"probabilities":{"a":0.95,"b":0.05}},"score":{"type":"score","score":0,"confidence":1,"probabilities":{"0":1,"1":0}},"noul":{"type":"noul","noul":0}},"usage":{"input_tokens":12,"output_tokens":3}}`))
	}))
	defer server.Close()
	response, err := NewClient("key", "", server.URL+"/v1").SystemOne(context.Background(), SystemOneRequest{
		State: "report", Questions: map[string]Question{
			"choice": {Type: "choice", Instructions: "Pick", Criteria: map[string]string{"a": "code", "b": "docs"}},
			"score":  {Type: "score", Instructions: "Rate", Criteria: []string{"low", "high"}},
			"noul":   {Type: "noul", Instructions: "Relevant?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Answers["choice"].Choice != "a" || response.Answers["choice"].Probabilities["a"] != 0.95 || response.Answers["score"].Score == nil || *response.Answers["score"].Score != 0 || response.Answers["noul"].Noul == nil || *response.Answers["noul"].Noul != 0 || response.Usage.InputTokens != 12 {
		t.Fatalf("response = %+v", response)
	}
}

func TestSystemOneMissingKeyAndHTTPError(t *testing.T) {
	t.Parallel()
	for _, status := range []int{401, 422, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = w.Write([]byte("upstream failure"))
			}))
			defer server.Close()
			_, err := NewClient(" \t", "", server.URL).SystemOne(context.Background(), SystemOneRequest{})
			if err == nil || !strings.Contains(err.Error(), "API key") || calls != 0 {
				t.Fatalf("missing key: calls=%d err=%v", calls, err)
			}
			_, err = NewClient("key", "", server.URL).SystemOne(context.Background(), SystemOneRequest{})
			if err == nil || !strings.Contains(err.Error(), "HTTP") || !strings.Contains(err.Error(), "upstream failure") {
				t.Fatalf("HTTP error = %v", err)
			}
			if llm.IsUnsafeForRetry(err) != (status == 401 || status == 422) {
				t.Fatalf("retry behavior changed: %v", err)
			}
		})
	}
}
