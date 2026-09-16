#!/usr/bin/env python3
"""Unit tests for local-speech-mlx.py with fake mlx-audio modules."""

from __future__ import annotations

import importlib.util
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from types import ModuleType
from unittest import mock


def load_helper_module():
    script = Path(__file__).with_name("local-speech-mlx.py")
    spec = importlib.util.spec_from_file_location("local_speech_mlx", script)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


class FakeSTTModel:
    def __init__(self, text: str):
        self.text = text
        self.calls = []

    def generate(self, audio_path, **kwargs):
        self.calls.append((audio_path, kwargs))
        return type("Result", (), {"text": self.text})()


class FakeTTSResult:
    def __init__(self, audio):
        self.audio = audio


class FakeKokoroModel:
    sample_rate = 24000

    def generate(self, text, voice, lang_code, **kwargs):
        yield FakeTTSResult([0.1, 0.2, 0.3])


class FakeQwenModel:
    sample_rate = 24000

    def __init__(self):
        self.generate_custom_voice = mock.Mock(
            return_value=iter([FakeTTSResult([0.4, 0.5, 0.6])])
        )


class LocalSpeechMLXTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.helper = load_helper_module()

    def test_require_mlx_audio_reports_import_error_as_json(self):
        with mock.patch.dict(sys.modules, {}, clear=True):
            with mock.patch("builtins.__import__", side_effect=ImportError("missing")):
                with mock.patch.object(sys, "stdout", new_callable=io.StringIO) as stdout:
                    with self.assertRaises(SystemExit) as ctx:
                        self.helper._require_mlx_audio()
        self.assertEqual(ctx.exception.code, 1)
        payload = json.loads(stdout.getvalue().strip())
        self.assertIn("mlx-audio is not installed", payload["error"])

    def test_stt_redirects_noisy_model_stdout(self):
        class NoisySTTModel(FakeSTTModel):
            def generate(self, audio_path, **kwargs):
                print("loading weights...", flush=True)
                return super().generate(audio_path, **kwargs)

        fake_model = NoisySTTModel("hello world")
        fake_load = mock.Mock(return_value=fake_model)
        fake_mlx_audio = ModuleType("mlx_audio")
        fake_stt_utils = ModuleType("mlx_audio.stt.utils")
        fake_stt_utils.load = fake_load
        with tempfile.NamedTemporaryFile(suffix=".wav") as audio:
            args = self.helper.build_parser().parse_args(
                [
                    "stt",
                    "--engine",
                    "parakeet",
                    "--audio",
                    audio.name,
                    "--model",
                    "test-model",
                ]
            )
            stdout_buf = io.StringIO()
            stderr_buf = io.StringIO()
            with mock.patch.dict(
                sys.modules,
                {
                    "mlx_audio": fake_mlx_audio,
                    "mlx_audio.stt.utils": fake_stt_utils,
                },
            ):
                saved_stdout = sys.stdout
                saved_stderr = sys.stderr
                try:
                    sys.stdout = stdout_buf
                    sys.stderr = stderr_buf
                    self.helper.cmd_stt(args)
                finally:
                    sys.stdout = saved_stdout
                    sys.stderr = saved_stderr
        self.assertIn("loading weights", stderr_buf.getvalue())
        payload = json.loads(stdout_buf.getvalue().strip())
        self.assertEqual(payload["text"], "hello world")

    def test_stt_ignores_prompt_and_language_kwargs(self):
        fake_model = FakeSTTModel("hello world")
        fake_load = mock.Mock(return_value=fake_model)
        fake_mlx_audio = ModuleType("mlx_audio")
        fake_stt_utils = ModuleType("mlx_audio.stt.utils")
        fake_stt_utils.load = fake_load
        with tempfile.NamedTemporaryFile(suffix=".wav") as audio:
            args = self.helper.build_parser().parse_args(
                [
                    "stt",
                    "--engine",
                    "parakeet",
                    "--audio",
                    audio.name,
                    "--model",
                    "test-model",
                    "--language",
                    "en",
                ]
            )
            with mock.patch.dict(
                sys.modules,
                {
                    "mlx_audio": fake_mlx_audio,
                    "mlx_audio.stt.utils": fake_stt_utils,
                },
            ):
                with mock.patch("builtins.print") as printed:
                    self.helper.cmd_stt(args)
        printed.assert_called_once()
        payload = json.loads(printed.call_args.args[0])
        self.assertEqual(payload["text"], "hello world")
        self.assertEqual(fake_model.calls, [(audio.name, {})])

    def test_tts_qwen_uses_generate_custom_voice(self):
        fake_model = FakeQwenModel()
        fake_load_model = mock.Mock(return_value=fake_model)
        fake_mlx_audio = ModuleType("mlx_audio")
        fake_tts_utils = ModuleType("mlx_audio.tts.utils")
        fake_tts_utils.load_model = fake_load_model
        fake_audio_io = ModuleType("mlx_audio.audio_io")
        fake_audio_io.write = mock.Mock()
        with tempfile.TemporaryDirectory() as tmpdir:
            output = Path(tmpdir) / "out.wav"
            args = self.helper.build_parser().parse_args(
                [
                    "tts",
                    "--engine",
                    "qwen3_tts",
                    "--text",
                    "hello",
                    "--output",
                    str(output),
                    "--model",
                    "mlx-community/Qwen3-TTS-12Hz-1.7B-CustomVoice-8bit",
                    "--voice",
                    "Ryan",
                    "--language",
                    "English",
                ]
            )
            with mock.patch.dict(
                sys.modules,
                {
                    "mlx_audio": fake_mlx_audio,
                    "mlx_audio.tts.utils": fake_tts_utils,
                    "mlx_audio.audio_io": fake_audio_io,
                },
            ):
                self.helper.cmd_tts(args)
        fake_model.generate_custom_voice.assert_called_once_with(
            text="hello",
            speaker="Ryan",
            language="English",
        )
        fake_audio_io.write.assert_called_once()


if __name__ == "__main__":
    unittest.main()
