package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	httpserver "github.com/A2gent/brute/internal/http"
	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
	"github.com/spf13/cobra"
)

func registerTestProvidersCmd(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "test-providers",
		Short: "List supported LLM providers and test connectivity",
		Long: `Diagnostic command for LLM provider setup.

By default talks to a running HTTP API server (brute server) so results match what
extensions and remote clients see. Use --local to run tests in-process without a server.`,
		RunE: runTestProviders,
	}
	cmd.Flags().IntVarP(&portFlag, "port", "p", 5445, "HTTP API server port (ignored with --local)")
	cmd.Flags().Bool("list-only", false, "Only list supported providers without running connectivity tests")
	cmd.Flags().Bool("local", false, "Run connectivity tests in-process instead of via HTTP API")
	root.AddCommand(cmd)
}

func runTestProviders(cmd *cobra.Command, args []string) error {
	loadDotEnv()

	printSupportedProvidersFromCode()

	listOnly, _ := cmd.Flags().GetBool("list-only")
	if listOnly {
		return nil
	}

	local, _ := cmd.Flags().GetBool("local")
	if local {
		return runProviderTestsLocal()
	}
	return runProviderTestsViaAPI(cmd)
}

func printSupportedProvidersFromCode() {
	fmt.Println("Supported providers (from code):")
	for _, def := range config.TestableProviders() {
		fmt.Printf("  %-16s %s\n", def.Type, def.DisplayName)
	}
}

func runProviderTestsViaAPI(cmd *cobra.Command) error {
	codeTypes := testableProviderTypes()

	port, _ := cmd.Flags().GetInt("port")
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	client := &http.Client{Timeout: 2 * time.Minute}

	listResp, err := client.Get(baseURL + "/providers")
	if err != nil {
		return fmt.Errorf("failed to reach API server at %s: %w (start with: brute server, or use --local)", baseURL, err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(listResp.Body)
		return fmt.Errorf("GET /providers failed (%d): %s", listResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var serverProviders []httpserver.ProviderConfigResponse
	if err := json.NewDecoder(listResp.Body).Decode(&serverProviders); err != nil {
		return fmt.Errorf("failed to decode /providers response: %w", err)
	}

	warnIfServerMissingProviders(codeTypes, serverProviders)

	fmt.Printf("\nProviders exposed by API server (%s):\n", baseURL)
	for _, p := range serverProviders {
		if p.Type == string(config.ProviderFallback) || p.Type == string(config.ProviderAutoRouter) {
			continue
		}
		status := "not configured"
		if p.Configured {
			status = "configured"
		}
		fmt.Printf("  %-16s %s (%s)\n", p.Type, p.DisplayName, status)
	}

	fmt.Println("\nRunning connectivity tests (POST /providers/test-all)...")
	testResp, err := client.Post(baseURL+"/providers/test-all", "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to run provider tests: %w", err)
	}
	defer testResp.Body.Close()
	body, err := io.ReadAll(testResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read test response: %w", err)
	}
	if testResp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /providers/test-all failed (%d): %s", testResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Results []httpserver.ProviderTestResult `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("failed to decode test response: %w", err)
	}

	return printProviderTestResults(payload.Results)
}

func runProviderTestsLocal() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	store, err := storage.NewSQLiteStore(cfg.DataPath)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	defer store.Close()

	applyCustomEnvFromStore(store)
	applyProviderEnvOverrides(cfg)

	llmClient, err := initLLMClient(cfg)
	if err != nil {
		// Match server startup: keep going so per-provider tests can report missing credentials.
		llmClient = anthropic.NewClientWithBaseURL("", cfg.DefaultModel, "https://api.kimi.com/coding/v1")
	}

	sessionManager := session.NewManager(store)
	toolManager := tools.NewManager(cfg.WorkDir)
	clipStore := speechcache.NewPersistent(0, filepath.Join(cfg.DataPath, "speech-clips"))
	integrationtools.Register(toolManager, store, clipStore, sessionManager)
	server := httpserver.NewServer(cfg, llmClient, toolManager, sessionManager, store, clipStore, 0)

	fmt.Println("\nProvider configuration (local):")
	for _, def := range config.TestableProviders() {
		status := "not configured"
		if server.ProviderConfiguredForUse(def.Type) {
			status = "configured"
		}
		fmt.Printf("  %-16s %s (%s)\n", def.Type, def.DisplayName, status)
	}

	fmt.Println("\nRunning connectivity tests (in-process)...")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	return printProviderTestResults(server.TestAllProviders(ctx))
}

func testableProviderTypes() map[string]struct{} {
	types := make(map[string]struct{})
	for _, def := range config.TestableProviders() {
		types[string(def.Type)] = struct{}{}
	}
	return types
}

func warnIfServerMissingProviders(codeTypes map[string]struct{}, serverProviders []httpserver.ProviderConfigResponse) {
	serverTypes := make(map[string]struct{}, len(serverProviders))
	for _, p := range serverProviders {
		serverTypes[p.Type] = struct{}{}
	}

	var missingOnServer []string
	for t := range codeTypes {
		if _, ok := serverTypes[t]; !ok {
			missingOnServer = append(missingOnServer, t)
		}
	}
	if len(missingOnServer) == 0 {
		return
	}

	sort.Strings(missingOnServer)
	fmt.Fprintf(os.Stderr, "\nWarning: running API server is missing providers present in code: %s\n", strings.Join(missingOnServer, ", "))
	fmt.Fprintln(os.Stderr, "Rebuild and restart the server to pick up new providers (e.g. go build ./cmd/aagent && brute server).")
}

func printProviderTestResults(results []httpserver.ProviderTestResult) error {
	fmt.Println("\nTest results:")
	passed := 0
	failed := 0
	for _, result := range results {
		status := "FAIL"
		if result.Success {
			status = "OK"
			passed++
		} else {
			failed++
		}
		message := strings.TrimSpace(result.Message)
		if len(message) > 120 {
			message = message[:117] + "..."
		}
		fmt.Printf("  %-16s %-4s %4dms  %s\n", result.Provider, status, result.Duration, message)
	}
	fmt.Printf("\nSummary: %d passed, %d failed, %d total\n", passed, failed, len(results))
	if failed > 0 {
		return fmt.Errorf("%d provider test(s) failed", failed)
	}
	return nil
}
