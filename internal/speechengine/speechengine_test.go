package speechengine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func isolateHostSpeechRuntime(t *testing.T) {
	t.Helper()
	t.Setenv("AAGENT_DATA_PATH", t.TempDir())
	t.Setenv("AAGENT_SPEECH_PYTHON", "")
	t.Setenv("AAGENT_SPEECH_WHISPERKIT_BIN", "")
	t.Setenv("AAGENT_WHISPERKIT_BIN", "")
	t.Setenv("AAGENT_WHISPER_BIN", "")
	p := &platformEnv{
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		dataPath: os.Getenv("AAGENT_DATA_PATH"),
		brewDirs: []string{t.TempDir()},
		lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		runner:   &fakeRunner{},
	}
	withTestPlatform(t, p)
}

func writeFakePythonHelper(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-local-speech-mlx.py")
	script := `#!/usr/bin/env python3
import argparse, json, struct, sys, time

parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd", required=True)

stt = sub.add_parser("stt")
stt.add_argument("--engine")
stt.add_argument("--audio")
stt.add_argument("--model")
stt.add_argument("--language", default="")
stt.add_argument("--prompt", default="")
stt.add_argument("--sleep", type=float, default=0)

tts = sub.add_parser("tts")
tts.add_argument("--engine")
tts.add_argument("--text")
tts.add_argument("--output")
tts.add_argument("--model")
tts.add_argument("--voice")
tts.add_argument("--lang-code")
tts.add_argument("--language", default="")
tts.add_argument("--instruct", default="")

args = parser.parse_args()

if args.cmd == "stt":
    if args.sleep:
        time.sleep(args.sleep)
    if args.engine == "moonshine" and args.language not in ("", "en"):
        print(json.dumps({"error": "moonshine supports English only"}))
        sys.exit(0)
    print(json.dumps({"text": "fake transcript from " + args.engine}))
    sys.exit(0)

if args.cmd == "tts":
    if args.engine == "qwen3_tts" and not args.language:
        print(json.dumps({"error": "language is required for qwen3_tts"}))
        sys.exit(0)
    # minimal mono PCM WAV (8-bit) for tests
    sample_rate = 8000
    seconds = 1
    samples = bytes([128] * sample_rate * seconds)
    data_size = len(samples)
    header = struct.pack(
        "<4sI4s4sIHHIIHH4sI",
        b"RIFF",
        36 + data_size,
        b"WAVE",
        b"fmt ",
        16,
        1,
        1,
        sample_rate,
        sample_rate,
        1,
        8,
        b"data",
        data_size,
    )
    with open(args.output, "wb") as fh:
        fh.write(header + samples)
    sys.exit(0)
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake python helper: %v", err)
	}
	return path
}

func writeFakePythonRunner(t *testing.T, dir, helperPath string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-python3")
	script := "#!/bin/sh\nscript=\"$1\"\nshift\nexec \"$script\" \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake python runner: %v", err)
	}
	return path
}

func writeFakeWhisperKitCLINoReport(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-whisperkit-cli-no-report")
	script := `#!/bin/sh
set -eu
echo "stdout-only transcript"
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake whisperkit cli without report: %v", err)
	}
	return path
}

func writeFakeWhisperKitCLI(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-whisperkit-cli")
	script := `#!/bin/sh
set -eu
report_dir=""
audio_path=""
model=""
task="transcribe"
language=""
prompt=""
sleep_seconds="0"

while [ $# -gt 0 ]; do
  case "$1" in
    --report-path)
      report_dir="$2"; shift 2;;
    --audio-path)
      audio_path="$2"; shift 2;;
    --model)
      model="$2"; shift 2;;
    --task)
      task="$2"; shift 2;;
    --language)
      language="$2"; shift 2;;
    --prompt)
      prompt="$2"; shift 2;;
    --sleep)
      sleep_seconds="$2"; shift 2;;
    --report|--verbose)
      shift;;
    transcribe)
      shift;;
    *)
      shift;;
  esac
done

if [ "$sleep_seconds" != "0" ]; then
  sleep "$sleep_seconds"
fi

if [ -z "$audio_path" ]; then
  echo "audio path required" >&2
  exit 1
fi

base="$(basename "$audio_path")"
base="${base%.*}"
mkdir -p "$report_dir"
if [ "$task" = "translate" ]; then
  text="translated transcript"
else
  text="whisperkit transcript"
fi
if [ -n "$language" ]; then
  text="$text lang=$language"
fi
if [ -n "$prompt" ]; then
  text="$text prompt=$prompt"
fi
printf '{"text":"%s","model":"%s"}' "$text" "$model" > "$report_dir/$base.json"
echo "$text"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake whisperkit cli: %v", err)
	}
	return path
}

func TestNormalizeLanguage(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: "auto", want: ""},
		{input: "en-US", want: "en"},
		{input: "ru_RU", want: "ru"},
	}
	for _, tc := range tests {
		if got := normalizeLanguage(tc.input); got != tc.want {
			t.Fatalf("normalizeLanguage(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestTranscribeRejectsUnknownEngine(t *testing.T) {
	_, err := Transcribe(context.Background(), "unknown", t.TempDir()+"/audio.wav", TranscribeOptions{})
	if !errors.Is(err, ErrInvalidEngineForTranscribe) {
		t.Fatalf("expected ErrInvalidEngineForTranscribe, got %v", err)
	}
}

func TestSynthesizeRejectsUnknownEngine(t *testing.T) {
	_, err := Synthesize(context.Background(), "parakeet", "hello")
	if !errors.Is(err, ErrInvalidEngineForSynthesize) {
		t.Fatalf("expected ErrInvalidEngineForSynthesize, got %v", err)
	}
}

func TestTranscribeParakeetWithFakeHelper(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	helper := writeFakePythonHelper(t, dir)
	python := writeFakePythonRunner(t, dir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	got, err := Transcribe(context.Background(), EngineParakeet, audioPath, TranscribeOptions{Language: "en"})
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if got != "fake transcript from parakeet" {
		t.Fatalf("unexpected transcript: %q", got)
	}
}

func TestTranscribeMoonshineRejectsNonEnglishLanguage(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	helper := writeFakePythonHelper(t, dir)
	python := writeFakePythonRunner(t, dir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	_, err := Transcribe(context.Background(), EngineMoonshine, audioPath, TranscribeOptions{Language: "ru"})
	if !errors.Is(err, ErrUnsupportedLanguage) {
		t.Fatalf("expected ErrUnsupportedLanguage, got %v", err)
	}
}

func TestTranscribeParakeetRejectsTranslation(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	translate := true
	_, err := Transcribe(context.Background(), EngineParakeet, audioPath, TranscribeOptions{TranslateToEnglish: &translate})
	if !errors.Is(err, ErrTranslationNotSupported) {
		t.Fatalf("expected ErrTranslationNotSupported, got %v", err)
	}
}

func TestTranscribeWhisperKitRejectsMissingReport(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	cli := writeFakeWhisperKitCLINoReport(t, dir)
	t.Setenv("AAGENT_SPEECH_WHISPERKIT_BIN", cli)

	_, err := Transcribe(context.Background(), EngineWhisperKit, audioPath, TranscribeOptions{})
	if !errors.Is(err, ErrWhisperKitReportMissing) {
		t.Fatalf("expected ErrWhisperKitReportMissing, got %v", err)
	}
}

func TestTranscribeWhisperKitWithFakeCLI(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	cli := writeFakeWhisperKitCLI(t, dir)
	t.Setenv("AAGENT_SPEECH_WHISPERKIT_BIN", cli)

	got, err := Transcribe(context.Background(), EngineWhisperKit, audioPath, TranscribeOptions{
		Language: "en",
		Prompt:   "context",
	})
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if !strings.Contains(got, "whisperkit transcript") {
		t.Fatalf("unexpected transcript: %q", got)
	}
}

func TestTranscribeWhisperKitSupportsTranslation(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	cli := writeFakeWhisperKitCLI(t, dir)
	t.Setenv("AAGENT_SPEECH_WHISPERKIT_BIN", cli)

	translate := true
	got, err := Transcribe(context.Background(), EngineWhisperKit, audioPath, TranscribeOptions{TranslateToEnglish: &translate})
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if got != "translated transcript" {
		t.Fatalf("unexpected transcript: %q", got)
	}
}

func TestTranscribeRespectsContextCancellation(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	helper := writeFakePythonHelper(t, dir)
	pythonPath := filepath.Join(dir, "slow-python3")
	script := "#!/bin/sh\nexec " + helper + " stt --engine parakeet --audio " + audioPath + " --model test --sleep 5\n"
	if err := os.WriteFile(pythonPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write slow python: %v", err)
	}
	t.Setenv("AAGENT_SPEECH_PYTHON", pythonPath)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := Transcribe(ctx, EngineParakeet, audioPath, TranscribeOptions{})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context error, got %v", err)
	}
}

func TestSynthesizeKokoroWithFakeHelper(t *testing.T) {
	dir := t.TempDir()
	helper := writeFakePythonHelper(t, dir)
	python := writeFakePythonRunner(t, dir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	audio, err := Synthesize(context.Background(), EngineKokoro, "hello")
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}
	if len(audio) < 12 || string(audio[:4]) != "RIFF" {
		t.Fatalf("expected WAV payload, got len=%d prefix=%q", len(audio), string(audio[:min(4, len(audio))]))
	}
}

func TestSynthesizeRejectsEmptyText(t *testing.T) {
	dir := t.TempDir()
	helper := writeFakePythonHelper(t, dir)
	python := writeFakePythonRunner(t, dir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	_, err := Synthesize(context.Background(), EngineQwen3TTS, "   ")
	if !errors.Is(err, ErrEmptyText) {
		t.Fatalf("expected ErrEmptyText, got %v", err)
	}
}

func TestTranscribeRequiresConfiguredPython(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	isolateHostSpeechRuntime(t)

	_, err := Transcribe(context.Background(), EngineParakeet, audioPath, TranscribeOptions{})
	if !errors.Is(err, ErrPythonNotConfigured) {
		t.Fatalf("expected ErrPythonNotConfigured, got %v", err)
	}
}

func TestTranscribeWhisperKitRequiresCLI(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	isolateHostSpeechRuntime(t)

	_, err := Transcribe(context.Background(), EngineWhisperKit, audioPath, TranscribeOptions{})
	if !errors.Is(err, ErrWhisperKitNotConfigured) {
		t.Fatalf("expected ErrWhisperKitNotConfigured, got %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestKokoroLangCodeMapping(t *testing.T) {
	got, err := kokoroLangCode("en")
	if err != nil || got != "a" {
		t.Fatalf("kokoroLangCode(en) = (%q, %v), want a", got, err)
	}
	_, err = kokoroLangCode("ru")
	if !errors.Is(err, ErrUnsupportedLanguage) {
		t.Fatalf("expected ErrUnsupportedLanguage for ru, got %v", err)
	}
}

func TestQwen3TTSLanguageMapping(t *testing.T) {
	got, err := qwen3TTSLanguage("ru")
	if err != nil || got != "Russian" {
		t.Fatalf("qwen3TTSLanguage(ru) = (%q, %v), want Russian", got, err)
	}
	got, err = qwen3TTSLanguage("en")
	if err != nil || got != "English" {
		t.Fatalf("qwen3TTSLanguage(en) = (%q, %v), want English", got, err)
	}
	got, err = qwen3TTSLanguage("Japanese")
	if err != nil || got != "Japanese" {
		t.Fatalf("qwen3TTSLanguage(Japanese) = (%q, %v), want Japanese", got, err)
	}
	got, err = qwen3TTSLanguage("chinese")
	if err != nil || got != "Chinese" {
		t.Fatalf("qwen3TTSLanguage(chinese) = (%q, %v), want Chinese", got, err)
	}
}

func TestSynthesizeWithLanguageKokoroMapsEnglish(t *testing.T) {
	dir := t.TempDir()
	helper := writeFakePythonHelper(t, dir)
	python := writeFakePythonRunner(t, dir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	audio, err := SynthesizeWithLanguage(context.Background(), EngineKokoro, "hello", "en")
	if err != nil {
		t.Fatalf("SynthesizeWithLanguage failed: %v", err)
	}
	if len(audio) < 12 || string(audio[:4]) != "RIFF" {
		t.Fatalf("expected WAV payload, got len=%d", len(audio))
	}
}

func TestSynthesizeWithLanguageKokoroRejectsRussian(t *testing.T) {
	dir := t.TempDir()
	helper := writeFakePythonHelper(t, dir)
	python := writeFakePythonRunner(t, dir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	_, err := SynthesizeWithLanguage(context.Background(), EngineKokoro, "hello", "ru")
	if !errors.Is(err, ErrUnsupportedLanguage) {
		t.Fatalf("expected ErrUnsupportedLanguage, got %v", err)
	}
}

func TestSynthesizeWithLanguageQwenRussian(t *testing.T) {
	dir := t.TempDir()
	helper := writeFakePythonHelper(t, dir)
	python := writeFakePythonRunner(t, dir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	audio, err := SynthesizeWithLanguage(context.Background(), EngineQwen3TTS, "hello", "ru")
	if err != nil {
		t.Fatalf("SynthesizeWithLanguage failed: %v", err)
	}
	if len(audio) < 12 || string(audio[:4]) != "RIFF" {
		t.Fatalf("expected WAV payload, got len=%d", len(audio))
	}
}

func TestAvailable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if Available(EngineParakeet) || Available(EngineWhisperKit) {
			t.Fatal("mlx/whisperkit engines should be unavailable off macOS")
		}
		return
	}
	isolateHostSpeechRuntime(t)

	if Available(EngineParakeet) || Available(EngineWhisperKit) || Available(EngineWhisperCPP) {
		t.Fatal("expected unavailable without configured runtimes")
	}

	t.Setenv("AAGENT_SPEECH_PYTHON", "/tmp/does-not-exist-aagent-python3")
	if Available(EngineKokoro) {
		t.Fatal("expected unavailable when configured python path does not exist")
	}

	if runtime.GOARCH == "arm64" {
		fakePython := filepath.Join(t.TempDir(), "fake-python3")
		if err := os.WriteFile(fakePython, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write fake python: %v", err)
		}
		t.Setenv("AAGENT_SPEECH_PYTHON", fakePython)
		if !Available(EngineKokoro) {
			t.Fatal("expected kokoro available when python path exists on darwin arm64")
		}
	}

	managed := filepath.Join(os.Getenv("AAGENT_DATA_PATH"), "speech", "whisper", "build", "bin", "whisper-cli")
	if err := os.MkdirAll(filepath.Dir(managed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !Available(EngineWhisperCPP) {
		t.Fatal("expected whisper.cpp available when managed whisper-cli exists")
	}
}

func TestEmbeddedMLXScriptCompiles(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	path, cleanup, err := materializeEmbeddedMLXScript()
	if err != nil {
		t.Fatalf("materializeEmbeddedMLXScript: %v", err)
	}
	defer cleanup()
	if err := exec.Command("python3", "-m", "py_compile", path).Run(); err != nil {
		t.Fatalf("python compile failed: %v", err)
	}
}

func TestMLXHelperPythonUnitTests(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join("scripts", "test_local_speech_mlx.py")
	cmd := exec.Command("python3", script)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python unit tests failed: %v\n%s", err, out)
	}
}

func TestParseWhisperKitReportArrayJSON(t *testing.T) {
	raw := []byte(`[{"text":"first"},{"text":"second"}]`)
	text, ok := parseWhisperKitReportJSON(raw)
	if !ok {
		t.Fatal("expected array report to parse")
	}
	if text != "first second" {
		t.Fatalf("unexpected text: %q", text)
	}
}

func TestMaterializeEmbeddedMLXScriptUsesUniquePaths(t *testing.T) {
	first, cleanupFirst, err := materializeEmbeddedMLXScript()
	if err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	defer cleanupFirst()
	second, cleanupSecond, err := materializeEmbeddedMLXScript()
	if err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	defer cleanupSecond()
	if first == second {
		t.Fatalf("expected unique temp script paths, got %q", first)
	}
}
