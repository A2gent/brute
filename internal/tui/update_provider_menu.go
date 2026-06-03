package tui

import (
	"github.com/A2gent/brute/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) updateProviderMenuKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.showProviderMenu {
		return m, nil, false
	}

	switch m.providerMenuStep {
	case 0:
		switch msg.Type {
		case tea.KeyEsc:
			m.showProviderMenu = false
			m.viewport.SetContent(m.renderMessages())
			return m, nil, true
		case tea.KeyUp:
			if m.providerMenuIndex > 0 {
				m.providerMenuIndex--
			}
			return m, nil, true
		case tea.KeyDown:
			providers := config.SupportedProviders()
			if m.providerMenuIndex < len(providers)-1 {
				m.providerMenuIndex++
			}
			return m, nil, true
		case tea.KeyEnter:
			providers := config.SupportedProviders()
			if m.providerMenuIndex < len(providers) {
				selectedProvider := providers[m.providerMenuIndex]
				model, cmd := m.selectProvider(selectedProvider.Type)
				return model.(Model), cmd, true
			}
			return m, nil, true
		default:
			return m, nil, true
		}
	case 1, 2:
		switch msg.Type {
		case tea.KeyEsc:
			m.showProviderMenu = false
			m.providerMenuStep = 0
			m.providerInput = ""
			m.viewport.SetContent(m.renderMessages())
			return m, nil, true
		case tea.KeyEnter:
			if m.providerInput != "" {
				model, cmd := m.saveProviderCredentials()
				return model.(Model), cmd, true
			}
			return m, nil, true
		case tea.KeyBackspace:
			if len(m.providerInput) > 0 {
				m.providerInput = m.providerInput[:len(m.providerInput)-1]
			}
			return m, nil, true
		case tea.KeyRunes:
			m.providerInput += string(msg.Runes)
			return m, nil, true
		case tea.KeySpace:
			m.providerInput += " "
			return m, nil, true
		default:
			return m, nil, true
		}
	default:
		return m, nil, true
	}
}

func (m Model) updateModelsMenuKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.showModelsMenu {
		return m, nil, false
	}

	switch msg.Type {
	case tea.KeyEsc:
		m.showModelsMenu = false
		m.viewport.SetContent(m.renderMessages())
		return m, nil, true
	case tea.KeyUp:
		if m.modelsMenuIndex > 0 {
			m.modelsMenuIndex--
		}
		return m, nil, true
	case tea.KeyDown:
		if m.modelsMenuIndex < len(m.availableModels)-1 {
			m.modelsMenuIndex++
		}
		return m, nil, true
	case tea.KeyEnter:
		if len(m.availableModels) > 0 && m.modelsMenuIndex < len(m.availableModels) {
			model, cmd := m.selectModel(m.availableModels[m.modelsMenuIndex])
			return model.(Model), cmd, true
		}
		return m, nil, true
	default:
		return m, nil, true
	}
}
