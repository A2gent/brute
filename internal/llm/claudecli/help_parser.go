package claudecli

import (
	"regexp"
	"strings"
)

var (
	helpQuotedModelRe = regexp.MustCompile(`'([a-z0-9][a-z0-9._-]*)'`)
	helpCommandLineRe = regexp.MustCompile(`^  ([a-z][a-z0-9_-]*(?:\|[a-z][a-z0-9_-]*)*)\b`)
)

func parseHelpOutput(raw string) (models []string, commands []string) {
	lines := strings.Split(raw, "\n")
	inCommands := false
	inModelOption := false
	seenModels := make(map[string]struct{})
	seenCommands := make(map[string]struct{})

	addUnique := func(out *[]string, seen map[string]struct{}, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		*out = append(*out, value)
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "commands:") {
			inCommands = true
			inModelOption = false
			continue
		}
		if inCommands {
			matches := helpCommandLineRe.FindStringSubmatch(line)
			if len(matches) != 2 {
				continue
			}
			for _, alias := range strings.Split(matches[1], "|") {
				addUnique(&commands, seenCommands, alias)
			}
			continue
		}

		if strings.HasPrefix(line, "  --model ") {
			inModelOption = true
		} else if strings.HasPrefix(line, "  -") {
			inModelOption = false
		}
		if !inModelOption {
			continue
		}
		for _, match := range helpQuotedModelRe.FindAllStringSubmatch(line, -1) {
			if len(match) == 2 {
				addUnique(&models, seenModels, match[1])
			}
		}
	}
	return models, commands
}
