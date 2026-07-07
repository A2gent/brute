package anthropic

import "testing"

func TestNewClientWithBaseURLDefaultsEmptyURL(t *testing.T) {
	client := NewClientWithBaseURL("test-key", "claude-test", "")
	if client.baseURL != defaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", client.baseURL, defaultBaseURL)
	}
}

func TestNewClientWithBaseURLNormalizesTrailingSlash(t *testing.T) {
	client := NewClientWithBaseURL("test-key", "claude-test", " https://proxy.example/v1/ ")
	if client.baseURL != "https://proxy.example/v1" {
		t.Fatalf("baseURL = %q, want %q", client.baseURL, "https://proxy.example/v1")
	}
}
