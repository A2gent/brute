# OpenAI API-key Responses transport

The `openai` provider uses `POST https://api.openai.com/v1/responses` in the HTTP
server, scheduler, TUI and CLI. Configured base URLs and `OPENAI_BASE_URL` remain
supported. Model discovery still uses the public `/models` catalog, not the
ChatGPT account catalog. API-key quota reporting and session prompt-cache status
are unchanged.

## Options and conversation state

`openaicodex.Options.PublicAPI` explicitly selects the public API contract.
ChatGPT account headers, the Codex instruction prefix, omission of token caps,
forced `store:false`, and omission of `service_tier:auto` remain ChatGPT-only.
Public requests forward the provider's reasoning effort, text verbosity,
service tier, prompt-cache key and maximum output tokens. Nonempty request-level
options override client defaults; the session ID is the fallback cache key.
Function tools explicitly use `strict:false` to preserve optional parameters.

- `stateful_responses` unset or false: `store:false`. Encrypted reasoning items
  are requested, saved with the assistant's session metadata and replayed before
  its function calls. This also works after a session reload. Items are bound to
  the provider-context epoch so a provider transition cannot replay old state.
- `stateful_responses:true`: `store:true` with `previous_response_id` continuation.
  The agent sends only messages after the stored response. This opts into OpenAI
  response storage; it is not enabled automatically.
- `max_tokens` maps to `max_output_tokens` for the public API only, including
  reasoning tokens. Leave it unset to avoid an artificial reasoning budget.
- Usage includes cached input tokens and reasoning output tokens, including
  incomplete streamed responses. Reasoning tokens are a subset of output tokens,
  not an additional billable count to add to them.

`gpt-4o` and `gpt-4.1` were verified against the public Responses API, so there is
no legacy Chat Completions fallback. Only set reasoning/verbosity options on
models that support those options.

## Real API verification

The opt-in integration test uses synthetic data only: three sequential
`lookup_record` calls followed by a sum. It runs the real Brute agent loop, saves
sessions in a temporary SQLite store and logs per-request latency and usage. Its
long synthetic reference prefix exceeds the prompt-cache threshold. Ordinary
unit tests never make paid API calls.

```sh
# Run from brute/. OPENAI_API_KEY must already be in the environment.
AAGENT_OPENAI_LIVE=1 go test -count=1 -tags=integration ./internal/agent \
  -run '^TestOpenAIResponsesLive' -v

# Compare another reasoning model.
AAGENT_OPENAI_LIVE=1 AAGENT_OPENAI_LIVE_MODEL=gpt-5-mini \
  go test -count=1 -tags=integration ./internal/agent \
  -run '^TestOpenAIResponsesLiveMultiToolBenchmark$' -v
```

Observed in one local run per model on 2026-10-14 (UTC):

| Model | Transport | Total latency | Input tokens | Cached input | Output tokens | Reasoning tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| gpt-5.4-mini | Previous Chat Completions client | 8.333 s | 18,647 | 12,672 | 67 | Not parsed |
| gpt-5.4-mini | Responses, stateless | 5.696 s | 18,426 | 12,288 | 152 | 73 |
| gpt-5.4-mini | Responses, stored | 4.875 s | 18,391 | 12,288 | 120 | 45 |
| gpt-5-mini | Previous Chat Completions client | 5.690 s | 18,623 | 13,312 | 195 | Not parsed |
| gpt-5-mini | Responses, stateless | 4.587 s | 18,431 | 12,544 | 169 | 64 |
| gpt-5-mini | Responses, stored | 6.506 s | 18,469 | 12,672 | 177 | 64 |

All six sessions made exactly three tool calls and returned `89`. Encrypted
reasoning was observed in the stateless Responses runs; the middle tool steps
on `gpt-5-mini` used zero new reasoning tokens. Separate live requests to
`gpt-4o` and `gpt-4.1` both returned `OK`.

These are smoke measurements, not a statistically controlled cost benchmark.
Responses uses the configured `reasoning:low` and `verbosity:low`; the old client
ignored those settings. Endpoint/model defaults, cache routing and network
warm-up differ, and the old parser does not report reasoning usage. Stateless
latency improved in both runs, but cached-token totals did not improve and the
stored variant was not consistently faster. Do not claim a guaranteed cost
reduction from these numbers. Representative repeated coding workloads and
actual model prices are needed for a cost conclusion.

## Remaining proxy limitation

Child/container calls using `A2GENT_PARENT_PROXY_URL` still use the existing
Chat Completions-compatible parent proxy. It does not carry encrypted reasoning
items or Responses continuation state. Direct-provider optimization does not
remove that limitation. Follow-up task T-146 covers the proxy contract and its
multi-tool regression test.
