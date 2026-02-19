package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/tools"
)

const (
	piperOutputModeStream = "stream"
	piperOutputModeFile   = "file"
	piperOutputModeBoth   = "both"
	piperDefaultExt       = ".wav"

	defaultPiperModelID   = "en_US-lessac-medium"
	defaultPiperPackage   = "piper-tts"
	defaultPiperVenvRel   = "tts/piper/runtime"
	defaultPiperModelsRel = "tts/piper/models"
)

var piperLanguageModelDefaults = map[string]string{
	"en": "en_US-lessac-medium",
	"ru": "ru_RU-ruslan-medium",
	"uk": "uk_UA-lada-x_low",
	"de": "de_DE-thorsten-medium",
	"fr": "fr_FR-siwis-medium",
	"es": "es_ES-sharvard-medium",
	"it": "it_IT-riccardo-x_low",
	"pt": "pt_BR-faber-medium",
	"ja": "ja_JP-kokoro-medium",
	"zh": "zh_CN-huayan-medium",
}

type PiperTTSTool struct {
	workDir   string
	clipStore *speechcache.Store
}

type piperTTSParams struct {
	Text          string   `json:"text"`
	PiperBin      string   `json:"piper_bin,omitempty"`
	ModelPath     string   `json:"model_path,omitempty"`
	ConfigPath    string   `json:"config_path,omitempty"`
	Language      string   `json:"language,omitempty"`
	SpeakerID     *int     `json:"speaker_id,omitempty"`
	LengthScale   *float64 `json:"length_scale,omitempty"`
	NoiseScale    *float64 `json:"noise_scale,omitempty"`
	NoiseW        *float64 `json:"noise_w,omitempty"`
	AutoModelPick *bool    `json:"auto_model_select,omitempty"`
	AutoSetup     *bool    `json:"auto_setup,omitempty"`
	OutputMode    string   `json:"output_mode,omitempty"`
	OutputPath    string   `json:"output_path,omitempty"`
	AutoPlayAudio *bool    `json:"auto_play_audio,omitempty"`
}

type piperRuntime struct {
	ExecPath    string
	PrefixArgs  []string
	Kind        string
	InstalledAt string
}

func NewPiperTTSTool(workDir string, clipStore *speechcache.Store) *PiperTTSTool {
	return &PiperTTSTool{workDir: workDir, clipStore: clipStore}
}

func (t *PiperTTSTool) Name() string {
	return "piper_tts"
}

func (t *PiperTTSTool) Description() string {
	return "Generate speech audio using local Piper ONNX models. Supports first-run auto-setup for runtime and model downloads."
}

func (t *PiperTTSTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to synthesize.",
			},
			"model_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to a Piper .onnx voice model, or a Piper model ID (for example: en_US-lessac-medium). Defaults to PIPER_MODEL_PATH, then PIPER_MODEL, then en_US-lessac-medium.",
			},
			"piper_bin": map[string]interface{}{
				"type":        "string",
				"description": "Optional path to piper executable. Defaults to PIPER_BIN then PATH/common Homebrew locations.",
			},
			"config_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional path to Piper model config JSON. Used only when model_path points to a local .onnx file.",
			},
			"language": map[string]interface{}{
				"type":        "string",
				"description": "Optional language hint (for example: en, ru, uk, de, fr, es, it, pt, ja, zh). Used when auto_model_select is enabled and model_path is not explicitly passed.",
			},
			"speaker_id": map[string]interface{}{
				"type":        "integer",
				"description": "Optional speaker ID for multi-speaker models.",
			},
			"length_scale": map[string]interface{}{
				"type":        "number",
				"description": "Optional Piper length scale. Lower values produce faster speech.",
			},
			"noise_scale": map[string]interface{}{
				"type":        "number",
				"description": "Optional Piper noise scale.",
			},
			"noise_w": map[string]interface{}{
				"type":        "number",
				"description": "Optional Piper noise width.",
			},
			"auto_setup": map[string]interface{}{
				"type":        "boolean",
				"description": "When true (default), automatically bootstrap a local Piper runtime on first use if no piper binary is found.",
			},
			"auto_model_select": map[string]interface{}{
				"type":        "boolean",
				"description": "When true (default), automatically pick a language-compatible Piper model from text/language hint if model_path is not provided.",
			},
			"output_mode": map[string]interface{}{
				"type":        "string",
				"description": "stream (default), file, or both.",
				"enum":        []string{piperOutputModeStream, piperOutputModeFile, piperOutputModeBoth},
			},
			"output_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional output file path when output_mode is file/both. Relative paths resolve from the active work directory.",
			},
			"auto_play_audio": map[string]interface{}{
				"type":        "boolean",
				"description": "When stream output is used, hint the webapp to auto-play the generated clip (default: true).",
			},
		},
		"required": []string{"text"},
	}
}

func (t *PiperTTSTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p piperTTSParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(p.OutputMode))
	if mode == "" {
		mode = piperOutputModeStream
	}
	if mode != piperOutputModeStream && mode != piperOutputModeFile && mode != piperOutputModeBoth {
		return &tools.Result{Success: false, Error: "output_mode must be one of: stream, file, both"}, nil
	}

	autoSetup := true
	if p.AutoSetup != nil {
		autoSetup = *p.AutoSetup
	}

	dataDir := resolveAAgentDataDir()
	runtimeSpec, err := resolvePiperRuntime(ctx, strings.TrimSpace(p.PiperBin), dataDir, autoSetup)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	modelArg, configPath, err := t.resolveModelReference(strings.TrimSpace(p.ModelPath), strings.TrimSpace(p.ConfigPath))
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	detectedLang := detectTextLanguage(text)
	selectedLang := normalizeLanguageHint(strings.TrimSpace(p.Language))
	if selectedLang == "" {
		selectedLang = detectedLang
	}
	autoModelSelect := true
	if p.AutoModelPick != nil {
		autoModelSelect = *p.AutoModelPick
	}
	modelSelectionSource := "explicit_or_env"
	if strings.TrimSpace(p.ModelPath) == "" && autoModelSelect {
		if autoModel := chooseAutoModelForLanguage(selectedLang); autoModel != "" {
			modelArg = autoModel
			configPath = ""
			modelSelectionSource = "auto_language"
		}
	}

	autoPlay := true
	if p.AutoPlayAudio != nil {
		autoPlay = *p.AutoPlayAudio
	}

	var outputPath string
	var cleanupPath string
	if mode == piperOutputModeStream {
		tmp, err := os.CreateTemp("", "a2gent-piper-*"+piperDefaultExt)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create temporary output file: %v", err)}, nil
		}
		cleanupPath = tmp.Name()
		outputPath = cleanupPath
		_ = tmp.Close()
	} else {
		resolved, err := t.resolveOutputPath(strings.TrimSpace(p.OutputPath))
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		outputPath = resolved
	}
	if cleanupPath != "" {
		defer os.Remove(cleanupPath)
	}

	modelsDir := filepath.Join(dataDir, defaultPiperModelsRel)
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create piper model directory: %v", err)}, nil
	}
	installedModels := listInstalledPiperModels(modelsDir)

	effectiveModelArg, effectiveConfigPath := resolveDownloadedVoicePaths(modelsDir, modelArg, configPath)

	inputFile, err := os.CreateTemp("", "a2gent-piper-input-*.txt")
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create temporary input file: %v", err)}, nil
	}
	inputPath := inputFile.Name()
	_ = inputFile.Close()
	defer os.Remove(inputPath)
	if err := os.WriteFile(inputPath, []byte(text+"\n"), 0o600); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to write synthesis input: %v", err)}, nil
	}

	buildArgs := func(modelValue string, cfgPath string) []string {
		args := make([]string, 0, 32)
		args = append(args, runtimeSpec.PrefixArgs...)
		args = append(args,
			"--model", modelValue,
			"--input_file", inputPath,
			"--output_file", outputPath,
			"--data-dir", modelsDir,
		)
		if cfgPath != "" {
			args = append(args, "--config", cfgPath)
		}
		if p.SpeakerID != nil {
			args = append(args, "--speaker", strconv.Itoa(*p.SpeakerID))
		}
		if p.LengthScale != nil {
			args = append(args, "--length_scale", formatFloat(*p.LengthScale))
		}
		if p.NoiseScale != nil {
			args = append(args, "--noise_scale", formatFloat(*p.NoiseScale))
		}
		if p.NoiseW != nil {
			args = append(args, "--noise_w", formatFloat(*p.NoiseW))
		}
		return args
	}

	runSynthesis := func() ([]byte, error) {
		cmd := exec.CommandContext(ctx, runtimeSpec.ExecPath, buildArgs(effectiveModelArg, effectiveConfigPath)...)
		return cmd.CombinedOutput()
	}

	downloadedVoice := false
	if out, err := runSynthesis(); err != nil {
		detail := strings.TrimSpace(string(out))
		voiceMissing := !strings.EqualFold(filepath.Ext(modelArg), ".onnx") && strings.Contains(detail, "Unable to find voice:")
		if voiceMissing && runtimeSpec.Kind == "python_module" {
			if dlErr := downloadPiperVoice(ctx, runtimeSpec.ExecPath, modelsDir, modelArg); dlErr == nil {
				downloadedVoice = true
				effectiveModelArg, effectiveConfigPath = resolveDownloadedVoicePaths(modelsDir, modelArg, configPath)
				if retryOut, retryErr := runSynthesis(); retryErr == nil {
					_ = retryOut
					err = nil
				} else {
					retryDetail := strings.TrimSpace(string(retryOut))
					if retryDetail != "" {
						return &tools.Result{Success: false, Error: fmt.Sprintf("piper failed after voice download: %v (%s)", retryErr, retryDetail)}, nil
					}
					return &tools.Result{Success: false, Error: fmt.Sprintf("piper failed after voice download: %v", retryErr)}, nil
				}
			}
		}
		if err == nil {
			goto synthesisRecovered
		}
		if detail != "" {
			return &tools.Result{Success: false, Error: fmt.Sprintf("piper failed: %v (%s)", err, detail)}, nil
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("piper failed: %v", err)}, nil
	}
synthesisRecovered:

	metadata := map[string]interface{}{
		"model_path":        effectiveModelArg,
		"runtime":           runtimeSpec.Kind,
		"runtime_exec":      runtimeSpec.ExecPath,
		"runtime_location":  runtimeSpec.InstalledAt,
		"models_dir":        modelsDir,
		"detected_language": detectedLang,
		"selected_language": selectedLang,
		"model_selection": map[string]interface{}{
			"source":              modelSelectionSource,
			"auto_model_select":   autoModelSelect,
			"language_model_map":  piperLanguageModelDefaults,
			"installed_model_ids": installedModels,
		},
	}
	if downloadedVoice {
		metadata["voice_downloaded"] = true
	}
	if effectiveConfigPath != "" {
		metadata["config_path"] = effectiveConfigPath
	}
	outputParts := []string{"Generated Piper speech audio."}

	if mode == piperOutputModeFile || mode == piperOutputModeBoth {
		metadata["file_output"] = map[string]interface{}{
			"path": outputPath,
		}
		outputParts = append(outputParts, "Saved file: "+outputPath)
	}

	if mode == piperOutputModeStream || mode == piperOutputModeBoth {
		if t.clipStore == nil {
			return &tools.Result{Success: false, Error: "speech clip cache is unavailable"}, nil
		}
		audio, err := os.ReadFile(outputPath)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read synthesized audio: %v", err)}, nil
		}
		if len(audio) == 0 {
			return &tools.Result{Success: false, Error: "generated audio is empty"}, nil
		}

		contentType := strings.TrimSpace(http.DetectContentType(audio))
		if contentType == "" || !strings.HasPrefix(contentType, "audio/") {
			contentType = "audio/wav"
		}

		clipID := t.clipStore.Save(contentType, audio)
		if clipID == "" {
			return &tools.Result{Success: false, Error: "failed to cache generated speech clip"}, nil
		}

		metadata["audio_clip"] = map[string]interface{}{
			"clip_id":        clipID,
			"content_type":   contentType,
			"auto_play":      autoPlay,
			"generated_with": "piper_tts",
		}
		outputParts = append(outputParts, "Clip ID: "+clipID)
	}

	return &tools.Result{
		Success:  true,
		Output:   strings.Join(outputParts, "\n"),
		Metadata: metadata,
	}, nil
}

func resolvePiperRuntime(ctx context.Context, requested string, dataDir string, autoSetup bool) (*piperRuntime, error) {
	if bin, ok := resolveLocalPiperBinary(requested); ok {
		return &piperRuntime{ExecPath: bin, Kind: "native_binary"}, nil
	}

	if !autoSetup {
		return nil, fmt.Errorf("piper binary not found and auto_setup is disabled")
	}

	python, err := findPython()
	if err != nil {
		return nil, fmt.Errorf("piper binary not found and Python runtime is unavailable for auto-setup: %v", err)
	}

	venvDir := filepath.Join(dataDir, defaultPiperVenvRel)
	pythonInVenv := venvPythonPath(venvDir)
	if _, err := os.Stat(pythonInVenv); err != nil {
		if mkErr := os.MkdirAll(venvDir, 0o755); mkErr != nil {
			return nil, fmt.Errorf("failed to create piper runtime directory: %v", mkErr)
		}
		if err := runCommand(ctx, python, "-m", "venv", venvDir); err != nil {
			return nil, fmt.Errorf("failed to create piper virtualenv: %v", err)
		}
	}

	if err := runCommand(ctx, pythonInVenv, "-m", "piper", "--help"); err != nil {
		if err := runCommand(ctx, pythonInVenv, "-m", "pip", "install", "--upgrade", "pip"); err != nil {
			return nil, fmt.Errorf("failed to upgrade pip for piper runtime: %v", err)
		}
		if err := runCommand(ctx, pythonInVenv, "-m", "pip", "install", defaultPiperPackage); err != nil {
			return nil, fmt.Errorf("failed to install piper runtime package: %v", err)
		}
		if err := runCommand(ctx, pythonInVenv, "-m", "piper", "--help"); err != nil {
			// Some environments have incomplete transitive installs; heal common missing modules.
			if err := runCommand(ctx, pythonInVenv, "-m", "pip", "install", "pathvalidate"); err != nil {
				return nil, fmt.Errorf("piper runtime was installed but is not runnable: %v", err)
			}
			if err := runCommand(ctx, pythonInVenv, "-m", "piper", "--help"); err != nil {
				return nil, fmt.Errorf("piper runtime was installed but is not runnable: %v", err)
			}
		}
	}

	return &piperRuntime{
		ExecPath:    pythonInVenv,
		PrefixArgs:  []string{"-m", "piper"},
		Kind:        "python_module",
		InstalledAt: venvDir,
	}, nil
}

func resolveLocalPiperBinary(requested string) (string, bool) {
	candidates := make([]string, 0, 5)
	if requested != "" {
		candidates = append(candidates, requested)
	}
	if env := strings.TrimSpace(os.Getenv("PIPER_BIN")); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, "piper", "/opt/homebrew/bin/piper", "/usr/local/bin/piper")

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, string(filepath.Separator)) {
			path := filepath.Clean(candidate)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, true
			}
			continue
		}
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved, true
		}
	}
	return "", false
}

func findPython() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("python3/python not found in PATH")
}

func venvPythonPath(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

func runCommand(ctx context.Context, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, detail)
	}
	return nil
}

func downloadPiperVoice(ctx context.Context, pythonExec string, modelsDir string, voiceName string) error {
	voice := strings.TrimSpace(voiceName)
	if voice == "" {
		return fmt.Errorf("voice name is required")
	}
	return runCommand(
		ctx,
		pythonExec,
		"-m", "piper.download_voices",
		"--download-dir", modelsDir,
		voice,
	)
}

func resolveAAgentDataDir() string {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_DATA_PATH")); raw != "" {
		return filepath.Clean(raw)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Clean(filepath.Join(".", ".aagent-data"))
	}
	return filepath.Join(homeDir, ".local", "share", "aagent")
}

func listInstalledPiperModels(modelsDir string) []string {
	out := make([]string, 0, 16)
	_ = filepath.WalkDir(modelsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".onnx") {
			return nil
		}
		id := strings.TrimSuffix(d.Name(), ".onnx")
		id = strings.TrimSpace(id)
		if id != "" {
			out = append(out, id)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func chooseAutoModelForLanguage(language string) string {
	lang := normalizeLanguageHint(language)
	if lang == "" {
		return ""
	}
	if model, ok := piperLanguageModelDefaults[lang]; ok {
		return model
	}
	return ""
}

func normalizeLanguageHint(raw string) string {
	lang := strings.ToLower(strings.TrimSpace(raw))
	if lang == "" {
		return ""
	}
	lang = strings.ReplaceAll(lang, "-", "_")
	switch {
	case strings.HasPrefix(lang, "en"):
		return "en"
	case strings.HasPrefix(lang, "ru"):
		return "ru"
	case strings.HasPrefix(lang, "uk"):
		return "uk"
	case strings.HasPrefix(lang, "de"):
		return "de"
	case strings.HasPrefix(lang, "fr"):
		return "fr"
	case strings.HasPrefix(lang, "es"):
		return "es"
	case strings.HasPrefix(lang, "it"):
		return "it"
	case strings.HasPrefix(lang, "pt"):
		return "pt"
	case strings.HasPrefix(lang, "ja"):
		return "ja"
	case strings.HasPrefix(lang, "zh"):
		return "zh"
	default:
		return ""
	}
}

func detectTextLanguage(text string) string {
	hasCyrillic := false
	hasUkrainianOnly := false
	hasHiraganaKatakana := false
	hasCJK := false
	hasLatinAccent := false

	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Cyrillic):
			hasCyrillic = true
			if strings.ContainsRune("іїєґІЇЄҐ", r) {
				hasUkrainianOnly = true
			}
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			hasHiraganaKatakana = true
		case unicode.In(r, unicode.Han):
			hasCJK = true
		case r >= 0x00C0 && r <= 0x024F:
			hasLatinAccent = true
		}
	}

	switch {
	case hasHiraganaKatakana:
		return "ja"
	case hasCJK:
		return "zh"
	case hasUkrainianOnly:
		return "uk"
	case hasCyrillic:
		return "ru"
	case hasLatinAccent:
		return "es"
	default:
		return "en"
	}
}

func (t *PiperTTSTool) resolveModelReference(modelInput string, configInput string) (string, string, error) {
	modelArg := strings.TrimSpace(modelInput)
	if modelArg == "" {
		modelArg = strings.TrimSpace(os.Getenv("PIPER_MODEL_PATH"))
	}
	if modelArg == "" {
		modelArg = strings.TrimSpace(os.Getenv("PIPER_MODEL"))
	}
	if modelArg == "" {
		modelArg = defaultPiperModelID
	}

	configPath := strings.TrimSpace(configInput)

	if strings.EqualFold(filepath.Ext(modelArg), ".onnx") {
		resolvedModelPath := t.resolvePath(modelArg)
		if _, err := os.Stat(resolvedModelPath); err != nil {
			return "", "", fmt.Errorf("model_path is not accessible: %v", err)
		}
		if configPath == "" {
			inferred := resolvedModelPath + ".json"
			if _, err := os.Stat(inferred); err == nil {
				configPath = inferred
			}
		}
		if configPath != "" {
			configPath = t.resolvePath(configPath)
			if _, err := os.Stat(configPath); err != nil {
				return "", "", fmt.Errorf("config_path is not accessible: %v", err)
			}
		}
		return resolvedModelPath, configPath, nil
	}

	if configPath != "" {
		return "", "", fmt.Errorf("config_path is only supported when model_path points to a local .onnx file")
	}

	return modelArg, "", nil
}

func resolveDownloadedVoicePaths(modelsDir string, modelArg string, configPath string) (string, string) {
	if strings.EqualFold(filepath.Ext(modelArg), ".onnx") {
		return modelArg, configPath
	}

	voiceModelPath := filepath.Join(modelsDir, modelArg+".onnx")
	if info, err := os.Stat(voiceModelPath); err == nil && !info.IsDir() {
		if strings.TrimSpace(configPath) != "" {
			return voiceModelPath, configPath
		}
		inferredConfig := voiceModelPath + ".json"
		if cfgInfo, cfgErr := os.Stat(inferredConfig); cfgErr == nil && !cfgInfo.IsDir() {
			return voiceModelPath, inferredConfig
		}
		return voiceModelPath, ""
	}
	return modelArg, configPath
}

func (t *PiperTTSTool) resolveOutputPath(requested string) (string, error) {
	baseDir := strings.TrimSpace(t.workDir)
	if baseDir == "" {
		baseDir = "."
	}

	if requested == "" {
		filename := fmt.Sprintf("a2gent-piper-%d%s", time.Now().UnixMilli(), piperDefaultExt)
		requested = filepath.Join(baseDir, filename)
	}

	path := t.resolvePath(requested)
	if filepath.Ext(path) == "" {
		path += piperDefaultExt
	}
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		return "", fmt.Errorf("unsupported output file extension %q for piper_tts (supported: .wav)", filepath.Ext(path))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}
	return path, nil
}

func (t *PiperTTSTool) resolvePath(path string) string {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return cleaned
	}
	if filepath.IsAbs(cleaned) {
		return filepath.Clean(cleaned)
	}
	baseDir := strings.TrimSpace(t.workDir)
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Clean(filepath.Join(baseDir, cleaned))
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

var _ tools.Tool = (*PiperTTSTool)(nil)
