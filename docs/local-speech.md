# Local Speech Engines

Brute exposes a Go package at `internal/speechengine` for on-device speech-to-text (STT) and text-to-speech (TTS). Processing stays local; first model download is allowed and handled by the upstream runtimes.

## Engine IDs

| ID | Type | Runtime | Default model / voice |
| --- | --- | --- | --- |
| `parakeet` | STT | mlx-audio (Python, macOS arm64) | `mlx-community/parakeet-tdt-0.6b-v3` |
| `moonshine` | STT | mlx-audio (Python, macOS arm64) | `UsefulSensors/moonshine-base` |
| `whisperkit` | STT | WhisperKit native CLI (macOS) | `large-v3-turbo` |
| `kokoro` | TTS | mlx-audio (Python, macOS arm64) | `mlx-community/Kokoro-82M-bf16` / `af_heart` |
| `qwen3_tts` | TTS | mlx-audio (Python, macOS arm64) | `mlx-community/Qwen3-TTS-12Hz-1.7B-CustomVoice-8bit` / `Ryan` |

Legacy `whisper_cpp` remains available through the same endpoint. In Caesar, open **Speech**, which splits **Local models** from **Cloud models (OpenRouter)**. Local STT defaults to Parakeet for both microphone and meeting transcription. TTS defaults to Piper; explicit local selections never fall back to cloud services. Cloud OpenRouter models appear only when an OpenRouter API key is configured in Providers, and they are used only when selected (`openrouter:<model-id>`). Existing explicit `whisper_stt` tool calls continue to use whisper.cpp.

`POST /speech/transcribe` accepts optional multipart `engine`; omission uses `AAGENT_STT_ENGINE`. `POST /speech/completion` accepts `model: "kokoro"` or `"qwen3_tts"`; omission uses `AAGENT_TTS_ENGINE`. Agent tools: `stt`, `kokoro_tts`, `qwen3_tts`.

## Go API

```go
text, err := speechengine.Transcribe(ctx, "parakeet", audioPath, speechengine.TranscribeOptions{
    Language: "en",
    Prompt:   "optional context",
})

wav, err := speechengine.Synthesize(ctx, "kokoro", "Hello from A²gent.")
wav, err := speechengine.SynthesizeWithLanguage(ctx, "qwen3_tts", "Привет", "ru")

if speechengine.Available("kokoro") {
    // python runtime is configured on this macOS host (models may still need download)
}
```

### Transcribe options

- `Language` - BCP-47 prefix (`en`, `ru`, …). Empty means auto/default.
- `TranslateToEnglish` - supported only by `whisperkit`.
- `Prompt` - passed to WhisperKit as `--prompt` (conditioning context). Parakeet/Moonshine ignore prompt and autodetect language.
- `Profile` - `meeting` selects a larger WhisperKit model.

### Synthesize language mapping

- `kokoro` - maps `en` → `a`, `es` → `e`, `fr` → `f`, `hi` → `h`, `it` → `i`, `pt` → `p`, `ja` → `j`, `zh` → `z`. Unsupported codes (e.g. `ru`) return `ErrUnsupportedLanguage`.
- `qwen3_tts` - maps ISO codes to Qwen language names (`en` → `English`, `ru` → `Russian`, …).

### Capability matrix

| Engine | Languages | Translate to English |
| --- | --- | --- |
| parakeet | 25 EU languages (v3), autodetect | no |
| moonshine | English only | no |
| whisperkit | Whisper language set | yes |
| kokoro | multilingual via `lang_code` | n/a |
| qwen3_tts | multilingual via CustomVoice `language` | n/a |

## Environment variables

| Variable | Purpose |
| --- | --- |
| `AAGENT_STT_ENGINE` | Default STT engine (`parakeet`) |
| `AAGENT_TTS_ENGINE` | Default TTS engine (`piper_tts`) |
| `AAGENT_WHISPERKIT_BIN` | WhisperKit CLI path (Caesar setting) |
| `PIPER_MODEL` | Legacy Piper voice/model |
| `AAGENT_WHISPER_LANGUAGE` | Default language for Whisper engines only |
| `AAGENT_WHISPER_TRANSLATE` | Translate to English with Whisper engines only |
| `AAGENT_SPEECH_PYTHON` | Python executable with mlx-audio installed |
| `AAGENT_SPEECH_MLX_SCRIPT` | Override path to `local-speech-mlx.py` (must exist; otherwise the embedded copy is used) |
| `AAGENT_SPEECH_PARAKEET_MODEL` | Parakeet Hugging Face repo id |
| `AAGENT_SPEECH_MOONSHINE_MODEL` | Moonshine Hugging Face repo id |
| `AAGENT_SPEECH_WHISPERKIT_BIN` | `whisperkit-cli` or `argmax-cli` path |
| `AAGENT_SPEECH_WHISPERKIT_MODEL` | WhisperKit `--model` value |
| `AAGENT_SPEECH_WHISPERKIT_MEETING_MODEL` | WhisperKit model when `Profile=meeting` |
| `AAGENT_SPEECH_KOKORO_MODEL` | Kokoro model repo id |
| `AAGENT_SPEECH_KOKORO_VOICE` | Kokoro voice preset (e.g. `af_heart`) |
| `AAGENT_SPEECH_KOKORO_LANG_CODE` | Kokoro language code (e.g. `a`) |
| `AAGENT_SPEECH_QWEN3_TTS_MODEL` | Qwen3-TTS CustomVoice model repo id |
| `AAGENT_SPEECH_QWEN3_TTS_VOICE` | Qwen3-TTS speaker name (e.g. `Ryan`) |
| `AAGENT_SPEECH_QWEN3_TTS_LANG_CODE` | Qwen3-TTS language hint (e.g. `English`) |

Brute does **not** run `pip install` during requests. Install dependencies ahead of time.

## Setup (macOS / Apple Silicon arm64)

mlx-audio engines require **Apple Silicon (arm64)**. Intel Macs can still use `whisperkit`.

```bash
brew install python ffmpeg whisperkit-cli
python3 -m venv .venv-speech
source .venv-speech/bin/activate
python -m pip install 'mlx-audio[stt,tts]==0.5.4' 'misaki[en]'
export AAGENT_SPEECH_PYTHON="$PWD/.venv-speech/bin/python"
./scripts/local-speech-check.sh
```

The mlx-audio 0.5.4 wheel includes all four model implementations. Model IDs were checked against Hugging Face; inference with downloaded weights still needs a host smoke test. The first download requires internet, but audio and text are processed on-device.

Point Go at your Python if it is not on PATH:

```bash
export AAGENT_SPEECH_PYTHON="$(which python3)"
```

## Helper script

`internal/speechengine/scripts/local-speech-mlx.py` is embedded by the Go package and materialized to a private temp file per invocation. The helper redirects mlx-audio progress prints to stderr so stdout stays a single JSON line. Override with `AAGENT_SPEECH_MLX_SCRIPT` only when pointing at an explicit on-disk copy for development:

```bash
python3 internal/speechengine/scripts/local-speech-mlx.py stt --engine parakeet --audio sample.wav --model mlx-community/parakeet-tdt-0.6b-v3
python3 internal/speechengine/scripts/local-speech-mlx.py tts --engine kokoro --text "Hello" --output /tmp/out.wav \
  --model mlx-community/Kokoro-82M-bf16 --voice af_heart --lang-code a
python3 internal/speechengine/scripts/local-speech-mlx.py tts --engine qwen3_tts --text "Hello" --output /tmp/out.wav \
  --model mlx-community/Qwen3-TTS-12Hz-1.7B-CustomVoice-8bit --voice Ryan --language English
```

WhisperKit example (upstream `argmax-cli` / `whisperkit-cli`; transcript is read from the JSON report, not stdout):

```bash
whisperkit-cli transcribe --audio-path sample.wav --model large-v3-turbo --report --report-path /tmp/wk-report
# optional conditioning context:
whisperkit-cli transcribe --audio-path sample.wav --model large-v3-turbo --prompt "Speaker names:" --report --report-path /tmp/wk-report
```

## Model download

First use of mlx-audio or WhisperKit downloads weights into the upstream cache (typically `~/.cache/huggingface` for mlx-audio and WhisperKit’s model store). Plan disk space accordingly:

- Parakeet v3 ≈ 600 MB
- Moonshine base ≈ 250 MB
- Kokoro bf16 ≈ 330 MB
- Qwen3-TTS CustomVoice 1.7B 8-bit ≈ 1–2 GB
- WhisperKit large-v3 ≈ 600 MB–1.5 GB depending on variant

## Errors

Common sentinel errors from the package:

- `ErrPythonNotConfigured` - set `AAGENT_SPEECH_PYTHON`
- `ErrMLXScriptNotFound` - script missing; set `AAGENT_SPEECH_MLX_SCRIPT`
- `ErrWhisperKitNotConfigured` - install CLI or set `AAGENT_SPEECH_WHISPERKIT_BIN`
- `ErrWhisperKitReportMissing` - CLI ran but no JSON report was written (stdout is not used as fallback)
- `ErrTranslationNotSupported` - translation requested for parakeet/moonshine
- `ErrUnsupportedLanguage` - e.g. non-English language with moonshine, or `ru` with kokoro
- `ErrEmptyTranscript` / `ErrEmptyText` - no usable output

Cancellation propagates via the passed `context.Context`.
