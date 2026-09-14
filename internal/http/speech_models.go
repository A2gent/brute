package http

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/A2gent/brute/internal/tools/integrationtools"
)

type speechModel struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Engine    string   `json:"engine,omitempty"`
	Voice     string   `json:"voice,omitempty"`
	Languages []string `json:"languages,omitempty"`
	Quality   string   `json:"quality,omitempty"`
	Available bool     `json:"available"`
}

type speechModelsResponse struct {
	Models []speechModel `json:"models"`
}

var recommendedSpeechModels = []speechModel{
	{ID: "auto", Label: "Auto (best available)", Languages: []string{"en", "ru"}, Quality: "auto", Available: true},
	{ID: "edge_tts:en-US-EmmaMultilingualNeural", Label: "Edge Emma multilingual", Engine: "edge_tts", Voice: "en-US-EmmaMultilingualNeural", Languages: []string{"en", "ru"}, Quality: "high"},
	{ID: "edge_tts:en-US-AndrewMultilingualNeural", Label: "Edge Andrew multilingual", Engine: "edge_tts", Voice: "en-US-AndrewMultilingualNeural", Languages: []string{"en", "ru"}, Quality: "high"},
	{ID: "edge_tts:en-US-AvaMultilingualNeural", Label: "Edge Ava multilingual", Engine: "edge_tts", Voice: "en-US-AvaMultilingualNeural", Languages: []string{"en", "ru"}, Quality: "high"},
	{ID: "edge_tts:ru-RU-DmitryNeural", Label: "Edge Dmitry (Russian)", Engine: "edge_tts", Voice: "ru-RU-DmitryNeural", Languages: []string{"ru"}, Quality: "medium"},
	{ID: "edge_tts:ru-RU-SvetlanaNeural", Label: "Edge Svetlana (Russian)", Engine: "edge_tts", Voice: "ru-RU-SvetlanaNeural", Languages: []string{"ru"}, Quality: "medium"},
	{ID: "piper_tts:en_US-ryan-high", Label: "Piper Ryan (English high)", Engine: "piper_tts", Voice: "en_US-ryan-high", Languages: []string{"en"}, Quality: "high"},
	{ID: "piper_tts:en_US-lessac-medium", Label: "Piper Lessac (English)", Engine: "piper_tts", Voice: "en_US-lessac-medium", Languages: []string{"en"}, Quality: "medium"},
	{ID: "piper_tts:ru_RU-ruslan-medium", Label: "Piper Ruslan (Russian)", Engine: "piper_tts", Voice: "ru_RU-ruslan-medium", Languages: []string{"ru"}, Quality: "medium"},
	{ID: "macos_say_tts:Samantha", Label: "macOS Samantha (English)", Engine: "macos_say_tts", Voice: "Samantha", Languages: []string{"en"}, Quality: "low"},
	{ID: "macos_say_tts:Milena", Label: "macOS Milena (Russian)", Engine: "macos_say_tts", Voice: "Milena", Languages: []string{"ru"}, Quality: "low"},
	{ID: "elevenlabs_tts:eleven_multilingual_v2", Label: "ElevenLabs multilingual v2", Engine: "elevenlabs_tts", Voice: "eleven_multilingual_v2", Languages: []string{"en", "ru"}, Quality: "high"},
	{ID: "elevenlabs_tts:eleven_turbo_v2_5", Label: "ElevenLabs turbo v2.5", Engine: "elevenlabs_tts", Voice: "eleven_turbo_v2_5", Languages: []string{"en", "ru"}, Quality: "high"},
}

func parseSpeechModel(id string) (string, string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" || strings.EqualFold(trimmed, "auto") {
		return "", "", nil
	}
	engine, voice, ok := strings.Cut(trimmed, ":")
	engine = strings.TrimSpace(engine)
	voice = strings.TrimSpace(voice)
	if !ok || engine == "" || voice == "" {
		return "", "", fmt.Errorf("model must be auto or engine:voice")
	}
	switch engine {
	case "edge_tts", "piper_tts", "macos_say_tts", "elevenlabs_tts":
		return engine, voice, nil
	default:
		return "", "", fmt.Errorf("unsupported speech engine %q", engine)
	}
}

func completionSpeechTools(modelID string) ([]string, error) {
	engine, _, err := parseSpeechModel(modelID)
	if err != nil {
		return nil, err
	}
	// WHY: Compact macOS voices are much worse than Edge/Piper neural models.
	// Prefer those first; Piper can auto-download a language model on first use.
	ranked := []string{"elevenlabs_tts", "edge_tts", "piper_tts", "macos_say_tts"}
	if engine == "" {
		return ranked, nil
	}
	out := []string{engine}
	for _, name := range ranked {
		if name != engine {
			out = append(out, name)
		}
	}
	return out, nil
}

func (s *Server) shouldSkipSpeechTool(toolName, modelID string) bool {
	engine, _, err := parseSpeechModel(modelID)
	if err != nil {
		return true
	}
	if engine == toolName {
		return false
	}
	switch toolName {
	case "elevenlabs_tts":
		return s == nil || s.resolveElevenLabsAPIKey() == ""
	case "macos_say_tts":
		return runtime.GOOS != "darwin"
	default:
		return false
	}
}

func (s *Server) handleListSpeechModels(w http.ResponseWriter, r *http.Request) {
	registered := func(name string) bool {
		if s == nil || s.toolManager == nil {
			return false
		}
		_, ok := s.toolManager.Get(name)
		return ok
	}
	edgeOK := registered("edge_tts") && integrationtools.EdgeTTSAvailable()
	piperOK := registered("piper_tts")
	macosOK := registered("macos_say_tts") && runtime.GOOS == "darwin"
	elevenOK := registered("elevenlabs_tts") && s.resolveElevenLabsAPIKey() != ""

	available := map[string]bool{
		"":               true,
		"edge_tts":       edgeOK,
		"piper_tts":      piperOK,
		"macos_say_tts":  macosOK,
		"elevenlabs_tts": elevenOK,
	}

	seen := map[string]struct{}{}
	out := make([]speechModel, 0, len(recommendedSpeechModels)+8)
	for _, model := range recommendedSpeechModels {
		if !available[model.Engine] {
			continue
		}
		model.Available = true
		out = append(out, model)
		seen[model.ID] = struct{}{}
	}

	if piperOK {
		for _, voice := range piperVoiceOptions() {
			id := "piper_tts:" + voice.ID
			if _, ok := seen[id]; ok {
				continue
			}
			out = append(out, speechModel{
				ID:        id,
				Label:     "Piper " + voice.ID,
				Engine:    "piper_tts",
				Voice:     voice.ID,
				Languages: piperModelLanguages(voice.ID),
				Quality:   "medium",
				Available: true,
			})
			seen[id] = struct{}{}
		}
	}

	s.jsonResponse(w, http.StatusOK, speechModelsResponse{Models: out})
}

func piperModelLanguages(id string) []string {
	lower := strings.ToLower(strings.TrimSpace(id))
	switch {
	case strings.HasPrefix(lower, "ru"):
		return []string{"ru"}
	case strings.HasPrefix(lower, "en"):
		return []string{"en"}
	default:
		return nil
	}
}
