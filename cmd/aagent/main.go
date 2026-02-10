package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gratheon/aagent/internal/agent"
	"github.com/gratheon/aagent/internal/config"
	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/llm/anthropic"
	"github.com/gratheon/aagent/internal/logging"
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
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "aagent [task]",
		Short: "A2gent - Autonomous AI coding agent with TUI",
		Long: `A2gent is a Go-based autonomous AI coding agent that executes tasks in sessions.
It features a TUI interface with scrollable history, multi-line input, and real-time status.`,
		Args: cobra.ArbitraryArgs,
		RunE: runAgent,
	}

	rootCmd.Flags().StringVarP(&modelFlag, "model", "m", "", "Override default model")
	rootCmd.Flags().StringVarP(&agentFlag, "agent", "a", "build", "Select agent type (build, plan)")
	rootCmd.Flags().StringVarP(&continueFlag, "continue", "c", "", "Resume previous session by ID")
	rootCmd.Flags().BoolVarP(&verboseFlag, "verbose", "v", false, "Verbose output")

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
	toolManager := tools.NewManager(cfg.WorkDir)

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
	agentConfig := agent.Config{
		Name:        agentFlag,
		Model:       cfg.DefaultModel,
		MaxSteps:    cfg.MaxSteps,
		Temperature: cfg.Temperature,
	}

	// Create TUI model
	model := tui.New(
		sess,
		sessionManager,
		agentConfig,
		llmClient,
		toolManager,
		initialTask,
	)

	// Run the TUI
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
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

	fmt.Printf("%-36s  %-10s  %-20s  %s\n", "ID", "Agent", "Created", "Status")
	fmt.Println("-------------------------------------------------------------------------------------")
	for _, s := range sessions {
		fmt.Printf("%-36s  %-10s  %-20s  %s\n", s.ID, s.AgentID, s.CreatedAt.Format("2006-01-02 15:04:05"), s.Status)
	}

	return nil
}
