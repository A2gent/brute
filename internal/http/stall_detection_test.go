package http

import (
	"testing"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/tools"
)

func TestStallDetectorOptInAndCredentials(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "jev-key"}, nil)
	if server.stallDetector() != nil {
		t.Fatal("stall checks must default off")
	}
	server.config.Tools.JevStall = config.JevStallConfig{Enabled: true, Interval: 4, Window: 6}
	detector := server.stallDetector()
	if detector == nil || detector.Client == nil || detector.Interval != 4 || detector.Window != 6 {
		t.Fatalf("configured detector=%+v", detector)
	}
	manager := tools.NewManager(t.TempDir())
	if server.newAgentFromConfig(agent.Config{}, nil, manager) == nil {
		t.Fatal("agent construction failed with stall checks enabled")
	}
	server.config.Providers["jev"] = config.Provider{}
	if server.stallDetector() != nil {
		t.Fatal("missing credentials must fail open")
	}
}
