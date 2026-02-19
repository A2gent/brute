package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
)

const speechTimeout = 10 * time.Second

const (
	completionAudioModeOff       = "off"
	completionAudioModeSystem    = "system"
	completionAudioModeElevenLab = "elevenlabs"
)

// SpeakCompletion announces task completion via sag, then falls back to macOS say.
func SpeakCompletion(message string) {
	if strings.TrimSpace(message) == "" {
		message = "Task complete"
	}

	mode := completionAudioMode()
	if mode == completionAudioModeOff {
		return
	}

	go func(msg string) {
		ctx, cancel := context.WithTimeout(context.Background(), speechTimeout)
		defer cancel()

		if trySag(ctx, msg) {
			return
		}
		if trySay(ctx, msg) {
			return
		}

		logging.Debug("No speech notifier available (sag and say unavailable or failed)")
	}(message)
}

func completionAudioMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("AAGENT_COMPLETION_AUDIO_MODE")))
	switch mode {
	case completionAudioModeOff, completionAudioModeSystem, completionAudioModeElevenLab:
		return mode
	case "":
		// Backward compatibility for existing boolean setting.
		v := strings.ToLower(strings.TrimSpace(os.Getenv("AAGENT_SPEECH_ENABLED")))
		if v == "1" || v == "true" || v == "yes" || v == "on" {
			return completionAudioModeSystem
		}
		return completionAudioModeOff
	default:
		// Fail-safe: unknown mode defaults to off.
		return completionAudioModeOff
	}
}

func trySag(ctx context.Context, message string) bool {
	if _, err := exec.LookPath("sag"); err != nil {
		return false
	}

	cmd := exec.CommandContext(ctx, "sag", message)
	if out, err := cmd.CombinedOutput(); err != nil {
		logging.Warn("sag notification failed: %v output=%s", err, strings.TrimSpace(string(out)))
		return false
	}
	return true
}

func trySay(ctx context.Context, message string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if _, err := exec.LookPath("say"); err != nil {
		return false
	}

	voice := strings.TrimSpace(os.Getenv("AAGENT_SAY_VOICE"))
	var cmd *exec.Cmd
	if voice != "" {
		cmd = exec.CommandContext(ctx, "say", "-v", voice, message)
	} else {
		cmd = exec.CommandContext(ctx, "say", message)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		logging.Warn("say notification failed: %v output=%s", err, strings.TrimSpace(string(out)))
		return false
	}
	return true
}

// BuildCompletionMessage creates a short completion phrase for speech.
func BuildCompletionMessage(prefix string, status string) string {
	p := strings.TrimSpace(prefix)
	s := strings.TrimSpace(status)
	if p == "" {
		p = "Agent run"
	}
	if s == "" {
		s = "completed"
	}
	return fmt.Sprintf("%s %s", p, s)
}
