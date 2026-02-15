FROM python:3.10-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    MOSS_REPO_PATH=/opt/MOSS-TTS \
    MOSS_SERVICE_PORT=8099

RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN git clone https://github.com/OpenMOSS/MOSS-TTS.git /opt/MOSS-TTS

WORKDIR /opt/MOSS-TTS

RUN python -m pip install --upgrade pip setuptools wheel

RUN python - <<'PY'
from pathlib import Path
import subprocess

req = Path('/opt/MOSS-TTS/moss_tts_realtime/requirements.txt')
filtered = Path('/tmp/moss_requirements.filtered.txt')
skip = (
    'cuda-bindings',
    'cuda-pathfinder',
    'cuda-python',
    'nvidia-',
    'flash-attn',
    'xformers',
    'triton',
    'torch',
    'torchaudio',
    'torchvision',
)

out = []
for raw in req.read_text(encoding='utf-8').splitlines():
    line = raw.strip()
    if not line or line.startswith('#'):
        continue
    low = line.lower()
    if any(low.startswith(prefix) for prefix in skip):
        continue
    out.append(line)

filtered.write_text('\n'.join(out) + '\n', encoding='utf-8')
subprocess.check_call(['python', '-m', 'pip', 'install', '-r', str(filtered)])
subprocess.check_call(['python', '-m', 'pip', 'install', 'numpy', 'torch', 'torchaudio'])
PY

WORKDIR /srv
COPY server.py /srv/server.py

EXPOSE 8099

CMD ["python", "/srv/server.py", "--host", "0.0.0.0", "--port", "8099"]
