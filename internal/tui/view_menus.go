package tui

import (
	"fmt"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/charmbracelet/lipgloss"
	"sort"
	"strings"
	"time"
)

func (m Model) renderCommandMenu() string {
	if !m.showCommandMenu || len(m.filteredCommands) == 0 {
		return ""
	}

	var items []string
	for i, cmd := range m.filteredCommands {
		item := fmt.Sprintf("/%s", cmd.Name)
		desc := commandDescStyle.Render(fmt.Sprintf(" - %s", cmd.Description))

		if i == m.commandMenuIndex {
			item = commandSelectedStyle.Render(item)
		} else {
			item = commandItemStyle.Render(item)
		}
		items = append(items, item+desc)
	}

	content := strings.Join(items, "\n")
	return commandMenuStyle.Render(content)
}

// renderSessionsList renders the sessions list popup
func (m Model) renderSessionsList() string {
	if !m.showSessionsList || len(m.availableSessions) == 0 {
		return ""
	}

	// Group sessions by day
	type sessionWithIndex struct {
		sess  *session.Session
		index int
	}

	grouped := make(map[string][]sessionWithIndex)
	for i, sess := range m.availableSessions {
		dayKey := sess.CreatedAt.Format("2006-01-02")
		grouped[dayKey] = append(grouped[dayKey], sessionWithIndex{sess: sess, index: i})
	}

	// Sort days in reverse chronological order
	var days []string
	for day := range grouped {
		days = append(days, day)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))

	// Build flat list of items with their types
	type listItem struct {
		isHeader bool
		day      string
		session  sessionWithIndex
	}

	var items []listItem
	for _, day := range days {
		items = append(items, listItem{isHeader: true, day: day})
		sessions := grouped[day]
		for _, s := range sessions {
			items = append(items, listItem{isHeader: false, session: s})
		}
	}

	// Calculate visible range based on scroll offset
	maxVisibleItems := m.height - 6 // Leave room for title, header, and borders
	if maxVisibleItems < 5 {
		maxVisibleItems = 5
	}

	// Ensure selected item is visible
	selectedItemIdx := 0
	for i, item := range items {
		if !item.isHeader && item.session.index == m.sessionsListIndex {
			selectedItemIdx = i
			break
		}
	}

	// Adjust offset to keep selected item in view
	if selectedItemIdx < m.sessionsListOffset {
		m.sessionsListOffset = selectedItemIdx
	} else if selectedItemIdx >= m.sessionsListOffset+maxVisibleItems {
		m.sessionsListOffset = selectedItemIdx - maxVisibleItems + 1
	}

	// Ensure offset doesn't go negative
	if m.sessionsListOffset < 0 {
		m.sessionsListOffset = 0
	}

	// Render visible items
	var rendered []string
	var headerText string
	if m.selectedProjectName != "" {
		headerText = fmt.Sprintf("Sessions for '%s' (Enter to switch, Esc to cancel):", m.selectedProjectName)
	} else {
		headerText = "Sessions (ungrouped) (Enter to switch, Esc to cancel):"
	}
	rendered = append(rendered, lipgloss.NewStyle().Bold(true).Render(headerText))
	rendered = append(rendered, "")

	// Calculate end index
	endIdx := m.sessionsListOffset + maxVisibleItems
	if endIdx > len(items) {
		endIdx = len(items)
	}

	for i := m.sessionsListOffset; i < endIdx; i++ {
		item := items[i]

		if item.isHeader {
			// Format day header
			day, _ := time.Parse("2006-01-02", item.day)
			today := time.Now().Truncate(24 * time.Hour)
			dayStart := day.Truncate(24 * time.Hour)

			var dayLabel string
			daysDiff := int(today.Sub(dayStart).Hours() / 24)
			switch daysDiff {
			case 0:
				dayLabel = "Today"
			case 1:
				dayLabel = "Yesterday"
			default:
				dayLabel = day.Format("Monday, Jan 2")
			}

			header := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#7D56F4")).
				Render("  " + dayLabel)
			rendered = append(rendered, header)
		} else {
			// Format session entry
			sess := item.session.sess
			title := sess.Title
			if title == "" {
				title = "(no title)"
			}
			if len(title) > 40 {
				title = title[:37] + "..."
			}

			current := ""
			if sess.ID == m.session.ID {
				current = " (current)"
			}

			childPrefix := ""
			if sess.ParentID != nil && strings.TrimSpace(*sess.ParentID) != "" {
				childPrefix = "↳ "
			}

			entry := fmt.Sprintf("    %s  %s%s%s",
				sess.CreatedAt.Format("15:04"),
				childPrefix,
				title,
				current,
			)

			if item.session.index == m.sessionsListIndex {
				entry = commandSelectedStyle.Render("  " + entry)
			} else {
				entry = commandItemStyle.Render("  " + entry)
			}
			rendered = append(rendered, entry)
		}
	}

	// Add scroll indicators if needed
	if m.sessionsListOffset > 0 {
		rendered[1] = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Render("  ▲ more above")
	}
	if endIdx < len(items) {
		rendered = append(rendered, lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Render("  ▼ more below"))
	}

	// Add help text
	help := "↑/↓: navigate  pgup/pgdn: page  home/end: top/bottom  enter: switch  esc: cancel"
	rendered = append(rendered, "")
	rendered = append(rendered, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render("  "+help))

	content := strings.Join(rendered, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}

// showProviderSelection shows the provider selection menu
func (m Model) renderAgentsMenu() string {
	if !m.showAgentsMenu {
		return ""
	}

	var items []string
	items = append(items, lipgloss.NewStyle().Bold(true).Render("Select Workflow / Agent (Enter creates a new session, Esc cancels):"))
	items = append(items, "")

	currentID := m.selectedWorkflow.ID
	if sessionWorkflow := workflowFromSessionMetadata(m.session); sessionWorkflow.ID != "" {
		currentID = sessionWorkflow.ID
	}
	for i, workflow := range m.availableWorkflows {
		baseLabel := fmt.Sprintf("  %s", workflow.Name)
		label := baseLabel
		if workflow.BuiltIn {
			label += commandDescStyle.Render(" [built-in]")
		}
		if workflow.ID == currentID {
			label += commandDescStyle.Render(" (current)")
		}
		target := workflowLaunchLabel(workflow)
		if target != "" && target != workflow.Name {
			label += commandDescStyle.Render(fmt.Sprintf(" -> %s", target))
		}
		if workflow.Description != "" {
			label += commandDescStyle.Render(fmt.Sprintf(" - %s", truncateLine(workflow.Description, 80)))
		}

		if i == m.agentsMenuIndex {
			label = commandSelectedStyle.Render(baseLabel) + strings.TrimPrefix(label, baseLabel)
		} else {
			label = commandItemStyle.Render(label)
		}
		items = append(items, label)
	}

	items = append(items, "")
	items = append(items, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render("  ↑/↓: navigate  enter: create session with workflow  esc: cancel"))

	content := strings.Join(items, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}

// renderProjectsMenu renders the projects selection menu
func (m Model) renderProjectsMenu() string {
	if !m.showProjectsMenu {
		return ""
	}

	var items []string
	items = append(items, lipgloss.NewStyle().Bold(true).Render("Select Project (Enter to confirm, Esc to cancel):"))
	items = append(items, "")

	// "No project" option
	noProjectLabel := "  (No project)"
	if m.projectsMenuIndex == 0 {
		noProjectLabel = commandSelectedStyle.Render(noProjectLabel)
	} else {
		noProjectLabel = commandItemStyle.Render(noProjectLabel)
	}
	items = append(items, noProjectLabel)

	// Project options
	for i, project := range m.availableProjects {
		label := fmt.Sprintf("  %s", project.Name)
		if project.Folder != nil && *project.Folder != "" {
			// Show folder path shortened
			folder := *project.Folder
			if len(folder) > 30 {
				folder = "..." + folder[len(folder)-27:]
			}
			label += commandDescStyle.Render(fmt.Sprintf(" (%s)", folder))
		}
		if project.IsSystem {
			label += commandDescStyle.Render(" [system]")
		}

		// +1 because index 0 is "No project"
		if m.projectsMenuIndex == i+1 {
			// Re-render with selection style
			labelBase := fmt.Sprintf("  %s", project.Name)
			label = commandSelectedStyle.Render(labelBase)
			if project.Folder != nil && *project.Folder != "" {
				folder := *project.Folder
				if len(folder) > 30 {
					folder = "..." + folder[len(folder)-27:]
				}
				label += commandDescStyle.Render(fmt.Sprintf(" (%s)", folder))
			}
			if project.IsSystem {
				label += commandDescStyle.Render(" [system]")
			}
		} else {
			label = commandItemStyle.Render(label)
		}
		items = append(items, label)
	}

	items = append(items, "")
	items = append(items, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render("  ↑/↓: navigate  enter: select  esc: cancel"))

	content := strings.Join(items, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}

// selectProvider handles provider selection and triggers credential prompts if needed
func (m Model) renderProviderMenu() string {
	if !m.showProviderMenu {
		return ""
	}

	var items []string

	switch m.providerMenuStep {
	case 0:
		// Provider selection
		items = append(items, lipgloss.NewStyle().Bold(true).Render("Select Provider (Enter to select, Esc to cancel):"))
		items = append(items, "")

		providers := config.SupportedProviders()
		for i, p := range providers {
			current := ""
			if string(p.Type) == m.appConfig.ActiveProvider {
				current = " (active)"
			}

			item := fmt.Sprintf("%s%s", p.DisplayName, current)
			if usageHint := m.providerUsageHint(p.Type); usageHint != "" {
				item = fmt.Sprintf("%s  %s", item, statsStyle.Render(usageHint))
			}

			if i == m.providerMenuIndex {
				item = commandSelectedStyle.Render(item)
			} else {
				item = commandItemStyle.Render(item)
			}
			items = append(items, item)
		}

	case 1:
		// API key input
		providerDef := config.GetProviderDefinition(config.ProviderType(m.selectedProviderType))
		name := m.selectedProviderType
		if providerDef != nil {
			name = providerDef.DisplayName
		}
		items = append(items, lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Enter API key for %s:", name)))
		items = append(items, "")
		// Show input with cursor (mask API key with asterisks for security)
		maskedInput := strings.Repeat("*", len(m.providerInput))
		cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Blink(true).Render("█")
		items = append(items, fmt.Sprintf("> %s%s", maskedInput, cursor))
		items = append(items, "")
		items = append(items, statsStyle.Render("(Press Enter to confirm, Esc to cancel)"))

	case 2:
		// URL input
		providerDef := config.GetProviderDefinition(config.ProviderType(m.selectedProviderType))
		name := m.selectedProviderType
		if providerDef != nil {
			name = providerDef.DisplayName
		}
		items = append(items, lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Enter URL for %s:", name)))
		items = append(items, "")
		// Show input with cursor
		cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Blink(true).Render("█")
		items = append(items, fmt.Sprintf("> %s%s", m.providerInput, cursor))
		items = append(items, "")
		items = append(items, statsStyle.Render("(Press Enter to confirm, Esc to cancel)"))
	}

	content := strings.Join(items, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}

// renderModelsMenu renders the model selection menu
func (m Model) renderModelsMenu() string {
	if !m.showModelsMenu || len(m.availableModels) == 0 {
		return ""
	}

	var items []string
	items = append(items, lipgloss.NewStyle().Bold(true).Render("Select Model (Enter to select, Esc to cancel):"))
	items = append(items, "")

	for i, model := range m.availableModels {
		current := ""
		if model == m.appConfig.DefaultModel {
			current = " (current)"
		}

		item := fmt.Sprintf("%s%s", model, current)

		if i == m.modelsMenuIndex {
			item = commandSelectedStyle.Render(item)
		} else {
			item = commandItemStyle.Render(item)
		}
		items = append(items, item)
	}

	content := strings.Join(items, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}
