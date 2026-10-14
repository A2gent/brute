package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTraceRecordsAnswersWithoutStateOrCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{"item_0":{"type":"score","score":0,"confidence":0.95}},"usage":{"input_tokens":12,"output_tokens":1}}`))
	}))
	defer server.Close()
	ctx, trace := WithTrace(context.Background())
	_, err := NewClient("private-key", "", server.URL).SystemOne(ctx, SystemOneRequest{State: "private-content", Questions: map[string]Question{"item_0": {Type: "score", Instructions: "private-instructions"}}})
	if err != nil {
		t.Fatal(err)
	}
	records := trace.Records()
	if len(records) != 1 || records[0].Status != "ok" || records[0].Response.Answers["item_0"].Score == nil || records[0].Questions["item_0"] != "score" {
		t.Fatalf("trace = %+v", records)
	}
	raw, _ := json.Marshal(records)
	for _, secret := range []string{"private-key", "private-content", "private-instructions"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("trace leaked %s", secret)
		}
	}
}

func TestTraceFailureDoesNotPersistServerBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("sensitive echoed request"))
	}))
	defer server.Close()
	ctx, trace := WithTrace(context.Background())
	_, _ = NewClient("key", "", server.URL).SystemOne(ctx, SystemOneRequest{State: "secret"})
	raw, _ := json.Marshal(trace.Records())
	if !strings.Contains(string(raw), `"status":"error"`) || strings.Contains(string(raw), "sensitive") {
		t.Fatalf("trace = %s", raw)
	}
}

func TestTraceDoesNotRetainUnexpectedResponseStrings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"echoed-secret","answers":{"item_0":{"type":"echoed-secret","choice":"echoed-secret","score":2,"confidence":0.9,"probabilities":{"echoed-secret":0.9}},"echoed-secret":{"type":"score","score":2}}}`))
	}))
	defer server.Close()
	ctx, trace := WithTrace(context.Background())
	_, err := NewClient("key", "", server.URL).SystemOne(ctx, SystemOneRequest{State: "secret", Questions: map[string]Question{"item_0": {Type: "score"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(trace.Records())
	if strings.Contains(string(raw), "echoed-secret") {
		t.Fatalf("unexpected upstream strings persisted: %s", raw)
	}
}
