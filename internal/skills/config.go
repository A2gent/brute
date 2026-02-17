package skills

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// LoadConfig loads skills configuration from skills.config.yaml
func LoadConfig(skillsFolder string) (*SkillConfig, error) {
	configPath := filepath.Join(skillsFolder, "skills.config.yaml")

	// Check if config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Return default config if file doesn't exist
		return DefaultConfig(), nil
	}

	// Read config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return DefaultConfig(), err
	}

	// Parse YAML
	var config SkillConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return DefaultConfig(), err
	}

	// Apply defaults for missing fields
	if config.Strategy.Default == "" {
		config.Strategy.Default = StrategyOnDemand
	}
	if config.MaxAutoLoadTokens == 0 {
		config.MaxAutoLoadTokens = 10000
	}

	return &config, nil
}

// GetSkillStrategy returns the loading strategy for a skill
func GetSkillStrategy(skillName string, config *SkillConfig) LoadStrategy {
	if settings, ok := config.Skills[skillName]; ok {
		return settings.Strategy
	}
	return config.Strategy.Default
}

// GetSkillPriority returns the priority for a skill (lower = higher priority)
func GetSkillPriority(skillName string, config *SkillConfig) int {
	if settings, ok := config.Skills[skillName]; ok {
		return settings.Priority
	}
	return 999 // Default low priority for unconfigured skills
}

// GetSkillTriggers returns trigger keywords for a skill
func GetSkillTriggers(skillName string, config *SkillConfig) []string {
	if !config.ProgressiveDisclosure.TriggerActivation.Enabled {
		return nil
	}

	if triggers, ok := config.ProgressiveDisclosure.TriggerActivation.Triggers[skillName]; ok {
		return triggers
	}
	return nil
}
