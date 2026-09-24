package speechengine

import "testing"

func TestBuildQwen3TTSInstruct(t *testing.T) {
	cfg := runtimeConfig{
		qwen3TTSModel:        defaultQwen3TTSModel,
		qwen3TTSStyleGender:  "female",
		qwen3TTSStylePitch:   "high",
		qwen3TTSStyleEmotion: "angry",
		qwen3TTSStyleSpeed:   "slow",
		qwen3TTSStyleExtra:   "like a news anchor",
	}
	got := buildQwen3TTSInstruct(cfg)
	want := "female voice, high pitch, very angry tone, speak slowly and calmly, like a news anchor"
	if got != want {
		t.Fatalf("buildQwen3TTSInstruct() = %q, want %q", got, want)
	}
}

func TestBuildQwen3TTSInstructIgnoredFor06B(t *testing.T) {
	cfg := runtimeConfig{
		qwen3TTSModel:       "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit",
		qwen3TTSStyleGender: "female",
	}
	if buildQwen3TTSInstruct(cfg) != "" {
		t.Fatal("expected empty instruct for 0.6B model")
	}
}

func TestQwen3ModelSupportsInstruct(t *testing.T) {
	if !qwen3ModelSupportsInstruct(defaultQwen3TTSModel) {
		t.Fatal("1.7B should support instruct")
	}
	if qwen3ModelSupportsInstruct("mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit") {
		t.Fatal("0.6B should not support instruct")
	}
}
