package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) updateQuestionKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.showQuestionPrompt || m.pendingQuestion == nil {
		return m, nil, false
	}

	var (
		taCmd tea.Cmd
		vpCmd tea.Cmd
		cmds  []tea.Cmd
	)

	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit, true
	case tea.KeyEsc:
		return m, nil, true
	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, vpCmd, true
	case tea.KeyUp:
		if msg.Alt {
			m.viewport, vpCmd = m.viewport.Update(msg)
			return m, vpCmd, true
		}
		if m.questionOptionIndex == -1 {
			m.questionOptionIndex = len(m.pendingQuestion.Options) - 1
		} else if m.questionOptionIndex > 0 {
			m.questionOptionIndex--
		}
		return m, nil, true
	case tea.KeyDown:
		if msg.Alt {
			m.viewport, vpCmd = m.viewport.Update(msg)
			return m, vpCmd, true
		}
		if m.questionOptionIndex < len(m.pendingQuestion.Options)-1 {
			m.questionOptionIndex++
		} else if m.pendingQuestion.Custom {
			m.questionOptionIndex = -1
		}
		return m, nil, true
	case tea.KeyEnter:
		var answer string
		if m.questionOptionIndex == -1 {
			answer = strings.TrimSpace(m.textarea.Value())
		} else if m.questionOptionIndex >= 0 && m.questionOptionIndex < len(m.pendingQuestion.Options) {
			answer = m.pendingQuestion.Options[m.questionOptionIndex].Label
		}

		if answer != "" {
			if err := m.sessionManager.AnswerQuestion(m.session.ID, answer); err != nil {
				m.messages = append(m.messages, message{
					role:      "error",
					content:   fmt.Sprintf("Failed to answer question: %v", err),
					timestamp: time.Now(),
				})
			} else {
				m.showQuestionPrompt = false
				m.pendingQuestion = nil
				m.textarea.Reset()
				m.updateViewportHeight()

				if sess, err := m.sessionManager.Get(m.session.ID); err == nil {
					m.session = sess
					if sess.Status == session.StatusRunning {
						m.processing = true
						m.lastUserInputTime = time.Now()
						cmd, cancel := m.runAgentResume()
						m.cancelFunc = cancel
						m.cancelPending = false
						cmds = append(cmds, cmd)
					}
				}
			}
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
		}
		return m, tea.Batch(cmds...), true
	case tea.KeyRunes:
		if len(msg.Runes) > 0 && msg.Runes[0] == '/' && m.textarea.Value() == "" && m.questionOptionIndex == -1 {
			m.showCommandMenu = true
			m.commandMenuIndex = 0
			m.filteredCommands = m.commandRegistry.GetCommands()
			return m, nil, true
		}
		if m.questionOptionIndex == -1 && m.pendingQuestion.Custom {
			m.textarea, taCmd = m.textarea.Update(msg)
			return m, taCmd, true
		}
		return m, nil, true
	default:
		if m.questionOptionIndex == -1 && m.pendingQuestion.Custom {
			m.textarea, taCmd = m.textarea.Update(msg)
			return m, taCmd, true
		}
		return m, nil, true
	}
}
