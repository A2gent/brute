import argparse
import importlib.util
import io
import os
import sys
import wave
from typing import List, Optional

import numpy as np
import torch
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse, Response
from pydantic import BaseModel, Field
from transformers import AutoModel, AutoTokenizer


class SynthesizeRequest(BaseModel):
    text: str
    model_id: str = "OpenMOSS-Team/MOSS-TTS-Realtime"
    device: Optional[str] = None
    reference_audio: List[str] = Field(default_factory=list)
    reference_text: List[str] = Field(default_factory=list)
    prompt_language: Optional[str] = None
    inference_language: Optional[str] = None


APP = FastAPI(title="moss-tts-realtime", version="1.0")


class Runtime:
    def __init__(self, repo_path: str, codec_model_id: str):
        self.repo_path = repo_path
        self.codec_model_id = codec_model_id
        self.models = {}

        if self.repo_path not in sys.path:
            sys.path.insert(0, self.repo_path)
        realtime_path = os.path.join(self.repo_path, "moss_tts_realtime")
        if realtime_path not in sys.path:
            sys.path.insert(0, realtime_path)

        try:
            from mossttsrealtime.modeling_mossttsrealtime import MossTTSRealtime
            from inferencer import MossTTSRealtimeInference
        except Exception as exc:
            raise RuntimeError(f"failed to import MOSS modules: {exc}") from exc

        self.MossTTSRealtime = MossTTSRealtime
        self.MossTTSRealtimeInference = MossTTSRealtimeInference

    def _resolve_device(self, requested: Optional[str]) -> str:
        if requested:
            return requested
        env = os.getenv("MOSS_DEVICE", "").strip()
        if env:
            return env
        return "cuda" if torch.cuda.is_available() else "cpu"

    def _resolve_attn_impl(self, device: str, dtype: torch.dtype) -> str:
        if (
            device == "cuda"
            and importlib.util.find_spec("flash_attn") is not None
            and dtype in {torch.float16, torch.bfloat16}
        ):
            major, _ = torch.cuda.get_device_capability()
            if major >= 8:
                return "flash_attention_2"
        if device == "cuda":
            return "sdpa"
        return "eager"

    def _load_model(self, model_id: str, device: str):
        key = (model_id, device)
        if key in self.models:
            return self.models[key]

        dtype = torch.bfloat16 if device == "cuda" else torch.float32
        attn_impl = self._resolve_attn_impl(device, dtype)

        model = self.MossTTSRealtime.from_pretrained(
            model_id,
            attn_implementation=attn_impl,
            torch_dtype=dtype,
        ).to(device)
        tokenizer = AutoTokenizer.from_pretrained(model_id)
        codec = AutoModel.from_pretrained(self.codec_model_id, trust_remote_code=True).eval().to(device)

        inferencer = self.MossTTSRealtimeInference(
            model,
            tokenizer,
            max_length=1000,
            codec=codec,
            codec_sample_rate=24000,
            codec_encode_kwargs={"chunk_duration": 8},
        )
        self.models[key] = (inferencer, codec)
        return inferencer, codec

    def synthesize(self, req: SynthesizeRequest) -> bytes:
        device = self._resolve_device(req.device)
        inferencer, codec = self._load_model(req.model_id, device)

        reference_audio_path = req.reference_audio[0] if req.reference_audio else None

        outputs = inferencer.generate(
            text=req.text,
            reference_audio_path=reference_audio_path,
            device=device,
        )
        if not outputs:
            raise RuntimeError("generation returned no outputs")

        generated_tokens = torch.tensor(outputs[0]).to(device)
        decode_result = codec.decode(generated_tokens.permute(1, 0), chunk_duration=8)
        wav = decode_result["audio"][0].detach().cpu()

        if wav.ndim == 1:
            wav = wav.unsqueeze(0)

        pcm = torch.clamp(wav, -1.0, 1.0).mul(32767.0).to(torch.int16).numpy()
        if pcm.ndim == 2:
            pcm = pcm.transpose(1, 0)
        if pcm.ndim == 1:
            pcm = pcm[:, None]

        buf = io.BytesIO()
        with wave.open(buf, "wb") as wf:
            wf.setnchannels(pcm.shape[1])
            wf.setsampwidth(2)
            wf.setframerate(24000)
            wf.writeframes(np.ascontiguousarray(pcm).tobytes())
        return buf.getvalue()


def build_runtime() -> Runtime:
    repo_path = os.getenv("MOSS_REPO_PATH", "/opt/MOSS-TTS")
    codec_model_id = os.getenv("MOSS_CODEC_MODEL_ID", "OpenMOSS-Team/MOSS-Audio-Tokenizer")
    return Runtime(repo_path=repo_path, codec_model_id=codec_model_id)


RUNTIME = build_runtime()


@APP.get("/health")
def health():
    return JSONResponse({"ok": True})


@APP.post("/synthesize")
def synthesize(req: SynthesizeRequest):
    text = req.text.strip()
    if not text:
        raise HTTPException(status_code=400, detail="text is required")

    if req.reference_text and req.reference_audio and len(req.reference_text) != len(req.reference_audio):
        raise HTTPException(status_code=400, detail="reference_audio and reference_text must have same length")

    try:
        payload = RUNTIME.synthesize(req)
    except Exception as exc:
        raise HTTPException(status_code=500, detail=str(exc))

    return Response(content=payload, media_type="audio/wav")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8099)
    args = parser.parse_args()

    import uvicorn

    uvicorn.run(APP, host=args.host, port=args.port, log_level="info")


if __name__ == "__main__":
    main()
