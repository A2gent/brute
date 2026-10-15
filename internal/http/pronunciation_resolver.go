package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/speechengine"
)

// defaultPronunciationSystemPrompt is editable in Settings -> Speech prompts.
// WHY: the LLM sees isolated lowercase tokens split out of paths, identifiers and commands
// (reLatin splits on "/", ".", "_", "-"), so the prompt explains that and how to read them.
const defaultPronunciationSystemPrompt = `You convert Latin-script tokens found inside Russian text into Russian Cyrillic readings for a TTS engine.
Tokens are isolated and lowercase, with no context. Many come from file paths and file names (speechengine, node_modules, tsconfig), file extensions (go, tsx, yml), CLI commands and flags, identifiers (camelCase, snake_case), URLs, package and product names.
Rules:
- Use American English pronunciation as developers say it aloud (suggest -> "сэдж+ест", cache -> "кэш", git -> "гит").
- Mark the stressed vowel with "+" right before it in every word with two or more syllables.
- Well-known tools and file extensions get their common spoken form (go -> "го", ts -> "ти эс", tsx -> "ти эс икс", py -> "пай", md -> "эм ди", yml -> "ямл", json -> "дж+ейсон", src -> "эс ар си").
- A token glued from several English words (speechengine, readme, localhost, dotfiles) is read as those words separated by a space ("спич +энджин").
- Abbreviations and tokens without vowels are spelled letter by letter, separated by spaces (cpu -> "си пи ю").
- Unknown names and invented words: read them the way an English speaker would sound them out.
- Output Cyrillic letters only, plus "+", spaces and hyphens. Never output Latin letters, digits or other punctuation.
Reply with ONLY a JSON object mapping each input token (lowercase, exactly as given) to its reading.`

// pronunciationSettings loads app settings for the resolver; an empty map keeps defaults on failure.
func (s *Server) pronunciationSettings() map[string]string {
	if s.store == nil {
		return map[string]string{}
	}
	settings, err := s.store.GetSettings()
	if err != nil || settings == nil {
		return map[string]string{}
	}
	return settings
}

// pronunciationClient picks the LLM for reading unknown words: the provider/model saved under
// Settings -> Speech prompts, falling back to the server default when unset or when the client cannot be built.
func (s *Server) pronunciationClient(settings map[string]string) (llm.Client, string) {
	model := ""
	if s.config != nil {
		model = s.config.DefaultModel
	}
	setting := promptLLMSettingsFromSettings(settings)[promptLLMCasePronunciation]
	if setting.Provider == "" || s.config == nil {
		return s.llmClient, model
	}
	provider := config.ProviderType(setting.Provider)
	chosen := setting.Model
	if chosen == "" {
		chosen = s.resolveModelForProvider(provider)
	}
	client, err := s.createLLMClient(provider, chosen, nil)
	if err != nil {
		return s.llmClient, model
	}
	return client, chosen
}

// installPronunciationResolver lets speech engines ask the default LLM how to read unknown
// English words; answers are cached on disk by the speechengine package.
func (s *Server) installPronunciationResolver() {
	if s.llmClient == nil {
		return
	}
	speechengine.PronunciationResolver = func(ctx context.Context, words []string) (map[string]string, error) {
		settings := s.pronunciationSettings()
		client, model := s.pronunciationClient(settings)
		resp, err := client.Chat(ctx, &llm.ChatRequest{
			Model:        model,
			SystemPrompt: serverPromptTemplatesFromSettings(settings).PronunciationSystemPrompt,
			Messages:     []llm.Message{{Role: "user", Content: strings.Join(words, "\n")}},
			Temperature:  0,
			MaxTokens:    1024,
		})
		if err != nil {
			return nil, err
		}
		content := resp.Content
		start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("no JSON in pronunciation reply")
		}
		out := map[string]string{}
		if err := json.Unmarshal([]byte(content[start:end+1]), &out); err != nil {
			return nil, err
		}
		return out, nil
	}
}
