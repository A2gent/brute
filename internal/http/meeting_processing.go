package http

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/stt/whispercpp"
)

const (
	defaultMeetingProcessingTimeout = 30 * time.Minute
	maxMeetingSummaryTokens         = 1200
)

func renderMeetingSummaryPrompt(template string, meeting meetingHistoryItem) string {
	return renderPromptTemplate(template, map[string]string{
		"title":      meeting.Title,
		"started_at": meeting.StartedAt,
		"ended_at":   meeting.EndedAt,
		"transcript": meeting.TranscriptMarkdown,
	})
}

func (s *Server) generateMeetingSummary(ctx context.Context, meeting meetingHistoryItem) (string, error) {
	if strings.TrimSpace(meeting.TranscriptMarkdown) == "" {
		return "", fmt.Errorf("meeting transcript is empty")
	}
	if s == nil || s.store == nil || s.config == nil {
		return "", fmt.Errorf("meeting summary service is unavailable")
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		return "", fmt.Errorf("load meeting summary settings: %w", err)
	}
	template := serverPromptTemplatesFromSettings(settings).MeetingSummaryPromptTemplate
	prompt := renderMeetingSummaryPrompt(template, meeting)
	targetConfig := s.resolvePromptLLMTarget(settings, promptLLMCaseMeetingSummary)
	target, err := s.resolveExecutionTarget(ctx, targetConfig.ProviderType, targetConfig.Model, prompt, nil)
	if err != nil {
		return "", fmt.Errorf("resolve meeting summary provider: %w", err)
	}

	response, err := target.Client.Chat(ctx, &llm.ChatRequest{
		Model: target.Model,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
		MaxTokens:   maxMeetingSummaryTokens,
	})
	if err != nil {
		return "", fmt.Errorf("generate meeting summary: %w", err)
	}
	summary := strings.TrimSpace(response.Content)
	if summary == "" {
		return "", fmt.Errorf("meeting summary provider returned empty content")
	}
	return summary, nil
}

func (s *Server) transcribeMeetingAudio(ctx context.Context, meeting meetingHistoryItem) (string, error) {
	if len(meeting.AudioPaths) == 0 {
		return "", fmt.Errorf("meeting has no audio files")
	}

	audioPaths := append([]string(nil), meeting.AudioPaths...)
	sort.Strings(audioPaths)
	entries := make([]string, 0, len(audioPaths))
	for _, audioPath := range audioPaths {
		absPath, err := filepath.Abs(strings.TrimSpace(audioPath))
		if err != nil {
			return "", fmt.Errorf("invalid audio path %q", audioPath)
		}
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() || info.Size() == 0 {
			return "", fmt.Errorf("meeting audio is unavailable: %s", absPath)
		}
		if info.Size() > maxMeetingAudioBytes {
			return "", fmt.Errorf("meeting audio is too large: %s", absPath)
		}

		whisperPath, cleanup, err := convertAudioToWAVForWhisper(ctx, absPath)
		if err != nil {
			return "", fmt.Errorf("prepare %s for transcription: %w", filepath.Base(absPath), err)
		}
		text, transcribeErr := whispercpp.TranscribeWithConfig(ctx, whisperPath, whispercpp.TranscribeOptions{Profile: "meeting"})
		if cleanup != nil {
			cleanup()
		}
		if transcribeErr != nil {
			return "", fmt.Errorf("transcribe %s: %w", filepath.Base(absPath), transcribeErr)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		speaker := meetingSpeakerLabelFromAudioPath(absPath)
		entries = append(entries, fmt.Sprintf("- [00:00:00] **%s:** %s", speaker, text))
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no speech detected in meeting audio")
	}
	return strings.Join(entries, "\n"), nil
}

func meetingSpeakerLabelFromAudioPath(audioPath string) string {
	stem := strings.TrimSuffix(filepath.Base(audioPath), filepath.Ext(audioPath))
	parts := strings.Split(stem, "-")
	if len(parts) == 0 {
		return "Speaker"
	}
	label := strings.TrimSpace(parts[len(parts)-1])
	if label == "" {
		return "Speaker"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}
