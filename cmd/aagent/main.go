package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gratheon/aagent/internal/agent"
	"github.com/gratheon/aagent/internal/config"
	httpserver "github.com/gratheon/aagent/internal/http"
	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/llm/anthropic"
	"github.com/gratheon/aagent/internal/llm/fallback"
	"github.com/gratheon/aagent/internal/llm/lmstudio"
	"github.com/gratheon/aagent/internal/logging"
	"github.com/gratheon/aagent/internal/scheduler"
	"github.com/gratheon/aagent/internal/session"
	"github.com/gratheon/aagent/internal/storage"
	"github.com/gratheon/aagent/internal/tools"
	"github.com/gratheon/aagent/internal/tui"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

var (
	modelFlag    string
	agentFlag    string
	continueFlag string
	verboseFlag  bool
	portFlag     int
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "aagent [task]",
		Short: "A2gent - Autonomous AI coding agent",
		Long: `A2gent is a Go-based autonomous AI coding agent that executes tasks in sessions.
Starts both the HTTP API server and the TUI interface simultaneously.`,
		Args: cobra.ArbitraryArgs,
		RunE: runAgentWithServer,
	}

	rootCmd.Flags().StringVarP(&modelFlag, "model", "m", "", "Override default model")
	rootCmd.Flags().StringVarP(&agentFlag, "agent", "a", "build", "Select agent type (build, plan)")
	rootCmd.Flags().StringVarP(&continueFlag, "continue", "c", "", "Resume previous session by ID")
	rootCmd.Flags().BoolVarP(&verboseFlag, "verbose", "v", false, "Verbose output")
	rootCmd.Flags().IntVarP(&portFlag, "port", "p", 8080, "HTTP API server port")

	// Session management subcommand
	sessionCmd := &cobra.Command{
		Use:   "session",
		Short: "Manage sessions",
	}

	sessionListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all sessions",
		RunE:  listSessions,
	}

	sessionCmd.AddCommand(sessionListCmd)
	rootCmd.AddCommand(sessionCmd)

	// Logs subcommand
	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "View or tail log files",
		RunE:  viewLogs,
	}
	logsCmd.Flags().BoolP("follow", "f", false, "Follow log output (like tail -f)")
	logsCmd.Flags().IntP("lines", "n", 50, "Number of lines to show")
	rootCmd.AddCommand(logsCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runAgentWithServer(cmd *cobra.Command, args []string) error {
	// Load .env files from common locations (ignore errors if not found)
	homeDir, _ := os.UserHomeDir()
	godotenv.Load(".env")                                  // Current directory
	godotenv.Load(filepath.Join(homeDir, ".env"))          // Home directory
	godotenv.Load(filepath.Join(homeDir, "git/mind/.env")) // Common project location

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize logging
	if err := logging.Init(cfg.DataPath); err != nil {
		return fmt.Errorf("failed to initialize logging: %w", err)
	}
	defer logging.Close()

	logging.Info("Starting aagent with HTTP server and TUI")

	// Override model if specified
	if modelFlag != "" {
		cfg.DefaultModel = modelFlag
	}

	// Initialize storage
	store, err := storage.NewSQLiteStore(cfg.DataPath)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	defer store.Close()
	if settings, err := store.GetSettings(); err == nil {
		applySettingsToEnv(settings)
	} else {
		logging.Warn("Failed to load persisted settings: %v", err)
	}

	// Initialize LLM client based on config
	llmClient, err := initLLMClient(cfg)
	if err != nil {
		// Don't fail - allow user to configure provider via /provider command
		logging.Warn("LLM client initialization failed: %v (use /provider to configure)", err)
		// Create a placeholder client that will be replaced when provider is configured
		llmClient = anthropic.NewClientWithBaseURL("", cfg.DefaultModel, "https://api.kimi.com/coding/v1")
	}

	// Initialize tool manager
	toolManager := tools.NewManagerWithStore(cfg.WorkDir, store)

	// Initialize session manager
	sessionManager := session.NewManager(store)

	// Start HTTP server in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := httpserver.NewServer(cfg, llmClient, toolManager, sessionManager, store, portFlag)
	go func() {
		logging.Info("Starting HTTP server on port %d", portFlag)
		if err := server.Run(ctx); err != nil && err.Error() != "http: Server closed" {
			logging.Error("HTTP server error: %v", err)
		}
	}()

	// Start scheduler for recurring jobs
	jobScheduler := scheduler.NewScheduler(store, sessionManager, llmClient, toolManager, cfg)
	jobScheduler.Start(ctx)
	defer jobScheduler.Stop()

	// Create or resume session for TUI
	var sess *session.Session
	if continueFlag != "" {
		sess, err = sessionManager.Get(continueFlag)
		if err != nil {
			logging.Error("Failed to resume session %s: %v", continueFlag, err)
			return fmt.Errorf("failed to resume session: %w", err)
		}
		logging.LogSession("resumed", sess.ID, fmt.Sprintf("agent=%s messages=%d", sess.AgentID, len(sess.Messages)))
	} else {
		sess, err = sessionManager.Create(agentFlag)
		if err != nil {
			logging.Error("Failed to create session: %v", err)
			return fmt.Errorf("failed to create session: %w", err)
		}
		logging.LogSession("created", sess.ID, fmt.Sprintf("agent=%s", agentFlag))
	}

	// Get initial task from args if provided
	var initialTask string
	if len(args) > 0 {
		initialTask = args[0]
		sess.AddUserMessage(initialTask)
	}

	// Create agent config
	contextWindow := 0
	if def := config.GetProviderDefinition(config.ProviderType(cfg.ActiveProvider)); def != nil {
		contextWindow = def.ContextWindow
	}
	agentConfig := agent.Config{
		Name:          agentFlag,
		Model:         cfg.DefaultModel,
		MaxSteps:      cfg.MaxSteps,
		Temperature:   cfg.Temperature,
		ContextWindow: contextWindow,
	}

	// Create TUI model
	tuiModel := tui.New(
		sess,
		sessionManager,
		agentConfig,
		llmClient,
		toolManager,
		initialTask,
		cfg,
	)

	// Run the TUI
	p := tea.NewProgram(
		tuiModel,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	// Cancel context to stop HTTP server
	cancel()

	return nil
}

func runAgent(cmd *cobra.Command, args []string) error {
	// Load .env files from common locations (ignore errors if not found)
	homeDir, _ := os.UserHomeDir()
	godotenv.Load(".env")                                  // Current directory
	godotenv.Load(filepath.Join(homeDir, ".env"))          // Home directory
	godotenv.Load(filepath.Join(homeDir, "git/mind/.env")) // Common project location

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize logging
	if err := logging.Init(cfg.DataPath); err != nil {
		return fmt.Errorf("failed to initialize logging: %w", err)
	}
	defer logging.Close()

	logging.Info("Starting aagent")

	// Override model if specified
	if modelFlag != "" {
		cfg.DefaultModel = modelFlag
	}

	// Get API key (support both KIMI_API_KEY and ANTHROPIC_API_KEY)
	apiKey := os.Getenv("KIMI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		logging.Error("KIMI_API_KEY or ANTHROPIC_API_KEY not set")
		return fmt.Errorf("KIMI_API_KEY or ANTHROPIC_API_KEY environment variable is required")
	}

	// Initialize storage
	store, err := storage.NewSQLiteStore(cfg.DataPath)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	defer store.Close()
	if settings, err := store.GetSettings(); err == nil {
		applySettingsToEnv(settings)
	} else {
		logging.Warn("Failed to load persisted settings: %v", err)
	}

	// Initialize LLM client
	// Use Kimi Code API (Anthropic-compatible) at https://api.kimi.com/coding/v1
	var llmClient llm.Client
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.kimi.com/coding/v1" // Default to Kimi Code API
	}
	logging.Info("Using LLM API: %s model=%s", baseURL, cfg.DefaultModel)
	llmClient = anthropic.NewClientWithBaseURL(apiKey, cfg.DefaultModel, baseURL)

	// Initialize tool manager
	toolManager := tools.NewManagerWithStore(cfg.WorkDir, store)

	// Initialize session manager
	sessionManager := session.NewManager(store)

	// Create or resume session
	var sess *session.Session
	if continueFlag != "" {
		sess, err = sessionManager.Get(continueFlag)
		if err != nil {
			logging.Error("Failed to resume session %s: %v", continueFlag, err)
			return fmt.Errorf("failed to resume session: %w", err)
		}
		logging.LogSession("resumed", sess.ID, fmt.Sprintf("agent=%s messages=%d", sess.AgentID, len(sess.Messages)))
	} else {
		sess, err = sessionManager.Create(agentFlag)
		if err != nil {
			logging.Error("Failed to create session: %v", err)
			return fmt.Errorf("failed to create session: %w", err)
		}
		logging.LogSession("created", sess.ID, fmt.Sprintf("agent=%s", agentFlag))
	}

	// Get initial task from args if provided
	var initialTask string
	if len(args) > 0 {
		initialTask = args[0]
		// Add the initial task to the session
		sess.AddUserMessage(initialTask)
	}

	// Create agent config
	contextWindow := 0
	if def := config.GetProviderDefinition(config.ProviderType(cfg.ActiveProvider)); def != nil {
		contextWindow = def.ContextWindow
	}
	agentConfig := agent.Config{
		Name:          agentFlag,
		Model:         cfg.DefaultModel,
		MaxSteps:      cfg.MaxSteps,
		Temperature:   cfg.Temperature,
		ContextWindow: contextWindow,
	}

	// Create TUI model
	tuiModel := tui.New(
		sess,
		sessionManager,
		agentConfig,
		llmClient,
		toolManager,
		initialTask,
		cfg,
	)

	// Run the TUI
	p := tea.NewProgram(
		tuiModel,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

func runServer(cmd *cobra.Command, args []string) error {
	// Load .env files from common locations (ignore errors if not found)
	homeDir, _ := os.UserHomeDir()
	godotenv.Load(".env")                                  // Current directory
	godotenv.Load(filepath.Join(homeDir, ".env"))          // Home directory
	godotenv.Load(filepath.Join(homeDir, "git/mind/.env")) // Common project location

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize logging
	if err := logging.Init(cfg.DataPath); err != nil {
		return fmt.Errorf("failed to initialize logging: %w", err)
	}
	defer logging.Close()

	logging.Info("Starting aagent HTTP server")

	// Get API key (support both KIMI_API_KEY and ANTHROPIC_API_KEY)
	apiKey := os.Getenv("KIMI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		logging.Error("KIMI_API_KEY or ANTHROPIC_API_KEY not set")
		return fmt.Errorf("KIMI_API_KEY or ANTHROPIC_API_KEY environment variable is required")
	}

	// Initialize storage
	store, err := storage.NewSQLiteStore(cfg.DataPath)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	defer store.Close()
	if settings, err := store.GetSettings(); err == nil {
		applySettingsToEnv(settings)
	} else {
		logging.Warn("Failed to load persisted settings: %v", err)
	}

	// Initialize LLM client
	var llmClient llm.Client
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.kimi.com/coding/v1" // Default to Kimi Code API
	}
	logging.Info("Using LLM API: %s model=%s", baseURL, cfg.DefaultModel)
	llmClient = anthropic.NewClientWithBaseURL(apiKey, cfg.DefaultModel, baseURL)

	// Initialize tool manager
	toolManager := tools.NewManagerWithStore(cfg.WorkDir, store)

	// Initialize session manager
	sessionManager := session.NewManager(store)

	// Create HTTP server
	server := httpserver.NewServer(cfg, llmClient, toolManager, sessionManager, store, portFlag)

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logging.Info("Received shutdown signal")
		cancel()
	}()

	// Start scheduler for recurring jobs
	jobScheduler := scheduler.NewScheduler(store, sessionManager, llmClient, toolManager, cfg)
	jobScheduler.Start(ctx)
	defer jobScheduler.Stop()

	// Run server
	if err := server.Run(ctx); err != nil && err.Error() != "http: Server closed" {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

func applySettingsToEnv(settings map[string]string) {
	for key, value := range settings {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if err := os.Setenv(k, value); err != nil {
			logging.Warn("Failed to set env var %q from settings: %v", k, err)
		}
	}
}

func viewLogs(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	follow, _ := cmd.Flags().GetBool("follow")
	lines, _ := cmd.Flags().GetInt("lines")

	logDir := cfg.DataPath + "/logs"

	// Find the most recent log file
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return fmt.Errorf("failed to read log directory: %w", err)
	}

	if len(entries) == 0 {
		fmt.Println("No log files found")
		return nil
	}

	// Get the most recent log file
	var latestLog string
	for _, entry := range entries {
		if !entry.IsDir() {
			latestLog = logDir + "/" + entry.Name()
		}
	}

	if latestLog == "" {
		fmt.Println("No log files found")
		return nil
	}

	fmt.Printf("Log file: %s\n\n", latestLog)

	if follow {
		// Use tail -f
		tailCmd := fmt.Sprintf("tail -f -n %d %s", lines, latestLog)
		fmt.Printf("Running: %s\n", tailCmd)
		fmt.Println("Press Ctrl+C to stop")
		return execCommand("tail", "-f", "-n", fmt.Sprintf("%d", lines), latestLog)
	}

	// Just show last N lines
	return execCommand("tail", "-n", fmt.Sprintf("%d", lines), latestLog)
}

func execCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func listSessions(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	store, err := storage.NewSQLiteStore(cfg.DataPath)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	defer store.Close()

	sessions, err := store.ListSessions()
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found")
		return nil
	}

	fmt.Printf("%-8s  %-20s  %-10s  %-30s\n", "ID", "Created", "Status", "Title")
	fmt.Println(strings.Repeat("-", 80))
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(no title)"
		}
		if len(title) > 30 {
			title = title[:27] + "..."
		}
		fmt.Printf("%-8s  %-20s  %-10s  %-30s\n", s.ID[:8], s.CreatedAt.Format("2006-01-02 15:04:05"), s.Status, title)
	}

	return nil
}

// initLLMClient initializes the LLM client based on config and environment
func initLLMClient(cfg *config.Config) (llm.Client, error) {
	resolveEnvKeys := func(providerType config.ProviderType) []string {
		switch providerType {
		case config.ProviderKimi:
			return []string{"KIMI_API_KEY"}
		case config.ProviderAnthropic:
			return []string{"ANTHROPIC_API_KEY"}
		case config.ProviderOpenRouter:
			return []string{"OPENROUTER_API_KEY"}
		case config.ProviderGoogle:
			return []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}
		default:
			return nil
		}
	}

	createDirectClient := func(providerType config.ProviderType, modelOverride string) (llm.Client, string, error) {
		providerDef := config.GetProviderDefinition(providerType)
		if providerDef == nil || providerType == config.ProviderFallback {
			return nil, "", fmt.Errorf("unsupported provider: %s", providerType)
		}

		provider := cfg.Providers[string(providerType)]
		apiKey := strings.TrimSpace(provider.APIKey)
		if apiKey == "" {
			for _, envKey := range resolveEnvKeys(providerType) {
				apiKey = strings.TrimSpace(os.Getenv(envKey))
				if apiKey != "" {
					break
				}
			}
		}

		baseURL := strings.TrimSpace(provider.BaseURL)
		if baseURL == "" {
			baseURL = strings.TrimSpace(providerDef.DefaultURL)
		}
		if envURL := strings.TrimSpace(os.Getenv(strings.ToUpper(string(providerType)) + "_BASE_URL")); envURL != "" {
			baseURL = envURL
		} else if envURL := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")); envURL != "" && (providerType == config.ProviderKimi || providerType == config.ProviderAnthropic) {
			baseURL = envURL
		}

		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(provider.Model)
		}
		if model == "" {
			model = strings.TrimSpace(providerDef.DefaultModel)
		}
		if model == "" {
			model = strings.TrimSpace(cfg.DefaultModel)
		}

		if providerDef.RequiresKey && apiKey == "" {
			envKeys := resolveEnvKeys(providerType)
			if len(envKeys) == 0 {
				return nil, "", fmt.Errorf("API key required for %s", providerDef.DisplayName)
			}
			return nil, "", fmt.Errorf("API key required for %s (set %s or use /provider)", providerDef.DisplayName, strings.Join(envKeys, " or "))
		}

		logging.Info("Using LLM provider: %s API: %s model=%s", providerType, baseURL, model)
		switch providerType {
		case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderGoogle:
			return lmstudio.NewClient(apiKey, model, baseURL), model, nil
		default:
			return anthropic.NewClientWithBaseURL(apiKey, model, baseURL), model, nil
		}
	}

	providerType := config.ProviderType(strings.TrimSpace(cfg.ActiveProvider))
	if providerType == config.ProviderFallback {
		fallbackCfg := cfg.Providers[string(config.ProviderFallback)]
		nodes := make([]fallback.Node, 0, len(fallbackCfg.FallbackChain))
		seen := make(map[string]struct{}, len(fallbackCfg.FallbackChain))
		for _, raw := range fallbackCfg.FallbackChain {
			nodeType := config.ProviderType(strings.ToLower(strings.TrimSpace(raw)))
			if nodeType == "" || nodeType == config.ProviderFallback {
				continue
			}
			if _, exists := seen[string(nodeType)]; exists {
				continue
			}
			seen[string(nodeType)] = struct{}{}
			client, model, err := createDirectClient(nodeType, "")
			if err != nil {
				return nil, fmt.Errorf("fallback node %s is not available: %w", nodeType, err)
			}
			nodes = append(nodes, fallback.Node{
				Name:   string(nodeType),
				Model:  model,
				Client: client,
			})
		}
		if len(nodes) < 2 {
			return nil, fmt.Errorf("fallback_chain requires at least two valid providers")
		}
		return fallback.NewClient(nodes), nil
	}

	client, _, err := createDirectClient(providerType, "")
	return client, err
}
