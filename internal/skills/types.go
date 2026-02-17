package skills

// LoadStrategy defines how a skill is loaded into the system prompt
type LoadStrategy string

const (
	StrategyAlways   LoadStrategy = "always"    // Load full content into system prompt
	StrategyOnDemand LoadStrategy = "on-demand" // Show only name+description, load when needed
	StrategyDisabled LoadStrategy = "disabled"  // Don't show the skill at all
)

// Skill represents a parsed skill with metadata and content
type Skill struct {
	// Frontmatter fields
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"`

	// Internal fields
	Path         string       // Absolute path to SKILL.md
	RelativePath string       // Relative path from skills folder
	Body         string       // Full markdown content (excluding frontmatter)
	Strategy     LoadStrategy // How to load this skill
	Priority     int          // Lower = higher priority
	Triggers     []string     // Keywords that activate this skill
}

// SkillConfig represents the skills.config.yaml file
type SkillConfig struct {
	Version               string                      `yaml:"version"`
	Strategy              StrategyConfig              `yaml:"strategy"`
	Skills                map[string]SkillSettings    `yaml:"skills"`
	MaxAutoLoadTokens     int                         `yaml:"max_auto_load_tokens"`
	ProgressiveDisclosure ProgressiveDisclosureConfig `yaml:"progressive_disclosure"`
	Clawhub               ClawhubConfig               `yaml:"clawhub"`
}

// StrategyConfig defines default strategy
type StrategyConfig struct {
	Default LoadStrategy `yaml:"default"`
}

// SkillSettings defines per-skill configuration
type SkillSettings struct {
	Strategy LoadStrategy `yaml:"strategy"`
	Priority int          `yaml:"priority"`
	Reason   string       `yaml:"reason"`
}

// ProgressiveDisclosureConfig configures progressive disclosure behavior
type ProgressiveDisclosureConfig struct {
	Enabled           bool                    `yaml:"enabled"`
	MetadataFields    []string                `yaml:"metadata_fields"`
	TriggerActivation TriggerActivationConfig `yaml:"trigger_activation"`
}

// TriggerActivationConfig configures automatic skill activation
type TriggerActivationConfig struct {
	Enabled  bool                `yaml:"enabled"`
	Triggers map[string][]string `yaml:"triggers"`
}

// ClawhubConfig configures clawhub.ai integration
type ClawhubConfig struct {
	Enabled        bool     `yaml:"enabled"`
	RegistryURL    string   `yaml:"registry_url"`
	AutoUpdate     bool     `yaml:"auto_update"`
	TrustedAuthors []string `yaml:"trusted_authors"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *SkillConfig {
	return &SkillConfig{
		Version: "1.0",
		Strategy: StrategyConfig{
			Default: StrategyOnDemand,
		},
		Skills:            make(map[string]SkillSettings),
		MaxAutoLoadTokens: 10000,
		ProgressiveDisclosure: ProgressiveDisclosureConfig{
			Enabled:        true,
			MetadataFields: []string{"name", "description"},
			TriggerActivation: TriggerActivationConfig{
				Enabled:  false, // Disabled by default
				Triggers: make(map[string][]string),
			},
		},
		Clawhub: ClawhubConfig{
			Enabled: false,
		},
	}
}
