#!/bin/sh
# Reproducible, offline corpus measurement. Run from any working directory.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if ! command -v go >/dev/null 2>&1; then
  echo 'Go 1.25+ is required; put its bin directory on PATH.' >&2
  exit 1
fi
# The test prints deterministic TSV token counts including the drop manifest.
# Executes the production wrapper using a local fake Jev HTTP server.
# Timing includes local HTTP/server setup, not production classifier latency.
go test ./internal/tools/integrationtools -run '^TestWebRelevanceTokenBenchmark$' -count=1 -v
go test ./internal/tools/integrationtools -run '^$' \
  -bench '^BenchmarkWebRelevanceTokens$' -benchmem -benchtime="${BENCHTIME:-100x}" -count=1
