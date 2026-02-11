// Package commands provides slash command handling for the TUI
package commands

// Command represents a slash command
type Command struct {
	Name        string
	Description string
	Aliases     []string
}

// Registry holds all available commands
type Registry struct {
	commands []Command
}

// NewRegistry creates a new command registry with default commands
func NewRegistry() *Registry {
	return &Registry{
		commands: []Command{
			{
				Name:        "new",
				Description: "Start a new session",
				Aliases:     []string{"n"},
			},
			{
				Name:        "sessions",
				Description: "List and switch sessions",
				Aliases:     []string{"s", "list"},
			},
			{
				Name:        "clear",
				Description: "Clear current conversation",
				Aliases:     []string{"c"},
			},
			{
				Name:        "help",
				Description: "Show available commands",
				Aliases:     []string{"h", "?"},
			},
		},
	}
}

// GetCommands returns all registered commands
func (r *Registry) GetCommands() []Command {
	return r.commands
}

// FindCommand finds a command by name or alias
func (r *Registry) FindCommand(input string) *Command {
	for i := range r.commands {
		if r.commands[i].Name == input {
			return &r.commands[i]
		}
		for _, alias := range r.commands[i].Aliases {
			if alias == input {
				return &r.commands[i]
			}
		}
	}
	return nil
}

// FilterCommands returns commands matching the given prefix
func (r *Registry) FilterCommands(prefix string) []Command {
	if prefix == "" {
		return r.commands
	}

	var matches []Command
	for _, cmd := range r.commands {
		if len(cmd.Name) >= len(prefix) && cmd.Name[:len(prefix)] == prefix {
			matches = append(matches, cmd)
			continue
		}
		for _, alias := range cmd.Aliases {
			if len(alias) >= len(prefix) && alias[:len(prefix)] == prefix {
				matches = append(matches, cmd)
				break
			}
		}
	}
	return matches
}
