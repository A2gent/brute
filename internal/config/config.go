package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds the application configuration
type Config struct {
	DefaultModel string              `json:"default_model"`
	MaxSteps     int                 `json:"max_steps"`
	Temperature  float64             `json:"temperature"`
	DataPath     string              `json:"data_path"`
	WorkDir      string              `json:"work_dir"`
	Providers    map[string]Provider `json:"providers"`
	Tools        ToolsConfig         `json:"tools"`
}

// Provider configuration for LLM providers
type Provider struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

// ToolsConfig configures tool permissions
type ToolsConfig struct {
	Bash  string `json:"bash"` // "allow", "deny", "ask"
	Read  string `json:"read"`
	Write string `json:"write"`
	Edit  string `json:"edit"`
	Glob  string `json:"glob"`
	Grep  string `json:"grep"`
	Task  string `json:"task"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	workDir, _ := os.Getwd()

	return &Config{
		DefaultModel: "kimi-for-coding",
		MaxSteps:     50,
		Temperature:  0.0,
		DataPath:     filepath.Join(homeDir, ".local", "share", "aagent"),
		WorkDir:      workDir,
		Providers:    make(map[string]Provider),
		Tools: ToolsConfig{
			Bash:  "allow",
			Read:  "allow",
			Write: "allow",
			Edit:  "allow",
			Glob:  "allow",
			Grep:  "allow",
			Task:  "allow",
		},
	}
}

// Load loads configuration from file and environment
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Try to load from config file
	configPaths := []string{
		".aagent/config.json",
		filepath.Join(os.Getenv("HOME"), ".config", "aagent", "config.json"),
	}

	for _, path := range configPaths {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, cfg); err != nil {
				return nil, err
			}
			break
		}
	}

	// Override with environment variables
	if model := os.Getenv("AAGENT_MODEL"); model != "" {
		cfg.DefaultModel = model
	}
	if dataPath := os.Getenv("AAGENT_DATA_PATH"); dataPath != "" {
		cfg.DataPath = dataPath
	}

	// Ensure data directory exists
	if err := os.MkdirAll(cfg.DataPath, 0755); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save saves configuration to file
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
