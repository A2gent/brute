#!/usr/bin/env python3
"""Local mlx-audio helper for A²gent brute speechengine package."""

from __future__ import annotations

import argparse
import contextlib
import json
import sys
from pathlib import Path


def _fail(message: str) -> None:
    print(json.dumps({"error": message}), file=sys.stdout)
    sys.exit(1)


@contextlib.contextmanager
def _redirect_inference_stdout_to_stderr():
    """mlx-audio load/generate often prints progress to stdout; keep JSON clean."""
    saved = sys.stdout
    sys.stdout = sys.stderr
    try:
        yield
    finally:
        sys.stdout = saved


def _require_mlx_audio() -> None:
    try:
        import mlx_audio  # noqa: F401
    except ImportError:
        _fail(
            "mlx-audio is not installed; run: pip install 'mlx-audio[stt,tts]' misaki"
        )


def cmd_stt(args: argparse.Namespace) -> None:
    _require_mlx_audio()
    audio_path = Path(args.audio)
    if not audio_path.is_file():
        _fail(f"audio file not found: {audio_path}")

    engine = args.engine.strip().lower()
    if engine == "moonshine" and args.language and args.language.lower() not in {"", "en"}:
        _fail("moonshine supports English only")

    try:
        from mlx_audio.stt.utils import load
    except ImportError:
        from mlx_audio.stt import load

    with _redirect_inference_stdout_to_stderr():
        model = load(args.model)
        # lean: parakeet/moonshine autodetect language; prompt is not supported here
        result = model.generate(str(audio_path))

    text = getattr(result, "text", "") or ""
    text = str(text).strip()
    if not text:
        _fail("no speech detected")
    print(json.dumps({"text": text}))


def cmd_tts(args: argparse.Namespace) -> None:
    _require_mlx_audio()
    text = (args.text or "").strip()
    if not text:
        _fail("text is required")

    output_path = Path(args.output)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    from mlx_audio.tts.utils import load_model
    from mlx_audio.audio_io import write

    with _redirect_inference_stdout_to_stderr():
        model = load_model(args.model)
        engine = args.engine.strip().lower()

        if engine == "kokoro":
            results = list(
                model.generate(
                    text=text,
                    voice=args.voice,
                    lang_code=args.lang_code,
                )
            )
        elif engine == "qwen3_tts":
            voice_kwargs = {
                "text": text,
                "speaker": args.voice,
                "language": args.language,
            }
            instruct = (args.instruct or "").strip()
            if instruct:
                voice_kwargs["instruct"] = instruct
            results = list(model.generate_custom_voice(**voice_kwargs))
        else:
            _fail(f"unsupported tts engine: {engine}")

    if not results:
        _fail("synthesis produced no audio")

    audio_chunks = []
    sample_rate = getattr(model, "sample_rate", 24000)
    for item in results:
        chunk = getattr(item, "audio", None)
        if chunk is not None:
            audio_chunks.append(chunk)

    if not audio_chunks:
        _fail("synthesis produced empty audio")

    if len(audio_chunks) == 1:
        audio = audio_chunks[0]
    else:
        import mlx.core as mx

        audio = mx.concatenate(audio_chunks)

    write(str(output_path), audio, sample_rate)



def cmd_silero_tts(args: argparse.Namespace) -> None:
    text = (args.text or "").strip()
    if not text:
        _fail("text is required")
    try:
        import torch
    except ImportError as exc:
        _fail(f"PyTorch is missing ({exc}); install the Silero TTS runtime")

    try:
        # v3+ models return (model, example_text); synthesis is the model's apply_tts method.
        model, _ = torch.hub.load(
            repo_or_dir="snakers4/silero-models",
            model="silero_tts",
            language=args.language,
            speaker=args.model,
            trust_repo=True,
        )
        audio = model.apply_tts(
            text=text,
            speaker=args.voice,
            sample_rate=args.sample_rate,
            put_accent=args.put_accent,
            put_yo=args.put_yo,
        )
    except Exception as exc:
        _fail(f"Silero TTS failed: {exc}")

    import numpy as np
    import wave

    samples = audio.detach().cpu().numpy().reshape(-1)
    pcm = (np.clip(samples, -1.0, 1.0) * 32767).astype(np.int16)
    output_path = Path(args.output)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(output_path), "wb") as output:
        output.setnchannels(1)
        output.setsampwidth(2)
        output.setframerate(args.sample_rate)
        output.writeframes(pcm.tobytes())


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="A²gent local speech mlx helper")
    sub = parser.add_subparsers(dest="command", required=True)

    stt = sub.add_parser("stt", help="Speech-to-text via mlx-audio")
    stt.add_argument("--engine", required=True, choices=["parakeet", "moonshine"])
    stt.add_argument("--audio", required=True)
    stt.add_argument("--model", required=True)
    stt.add_argument("--language", default="")
    stt.set_defaults(func=cmd_stt)

    tts = sub.add_parser("tts", help="Text-to-speech via mlx-audio")
    tts.add_argument("--engine", required=True, choices=["kokoro", "qwen3_tts"])
    tts.add_argument("--text", required=True)
    tts.add_argument("--output", required=True)
    tts.add_argument("--model", required=True)
    tts.add_argument("--voice", required=True)
    tts.add_argument("--lang-code", default="")
    tts.add_argument("--language", default="")
    tts.add_argument(
        "--instruct",
        default="",
        help="Optional natural-language style control for Qwen3 CustomVoice 1.7B",
    )
    tts.set_defaults(func=cmd_tts)

    silero = sub.add_parser("silero-tts", help="Text-to-speech via Silero Torch Hub")
    silero.add_argument("--text", required=True)
    silero.add_argument("--output", required=True)
    silero.add_argument("--model", default="v5_5_ru")
    silero.add_argument("--voice", default="xenia")
    silero.add_argument("--language", default="ru")
    silero.add_argument("--sample-rate", type=int, default=48000, choices=[8000, 16000, 24000, 48000])
    silero.add_argument("--put-accent", action=argparse.BooleanOptionalAction, default=True)
    silero.add_argument("--put-yo", action=argparse.BooleanOptionalAction, default=True)
    silero.set_defaults(func=cmd_silero_tts)

    return parser


def main() -> None:
    parser = build_parser()
    args = parser.parse_args()
    if args.command == "tts" and args.engine == "kokoro" and not args.lang_code:
        _fail("lang-code is required for kokoro")
    if args.command == "tts" and args.engine == "qwen3_tts" and not args.language:
        _fail("language is required for qwen3_tts")
    args.func(args)


if __name__ == "__main__":
    main()
