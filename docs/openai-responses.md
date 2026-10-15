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

## Auxiliary one-shot requests

Session summaries, commit messages, PR descriptions, meeting summaries, and the
HTTP automatic-router classifier override the `openai` provider's reasoning
default with the lowest model-supported effort. Explicit router effort settings
still take precedence. Other providers are unchanged.

- Original GPT-5 / mini / nano: `minimal`.
- GPT-5.1 and newer general-purpose models, GPT-6 Sol / Luna: `none`.
- GPT-6 Astra / GPT-6.1 Sol and the original GPT-5 Codex variants: `low`.
- GPT-5.1 Codex Mini: `medium`; GPT-5 Pro: `high`; later GPT-5 Pro: `medium`.
- GPT-4.x and other non-reasoning models: no effort override.

These values follow the current OpenAI [reasoning guide](https://developers.openai.com/api/docs/guides/reasoning#reasoning-effort),
[GPT-5 guide](https://developers.openai.com/api/docs/guides/latest-model/gpt-5),
[GPT-5.4 Mini model page](https://developers.openai.com/api/docs/models/gpt-5.4-mini),
and the individual model pages. In particular, `none` is not valid for every
GPT-6, Codex, or Pro model.

Session and meeting summaries request `service_tier: flex` only with `openai`.
The public Responses client retries an HTTP 429 from Flex once using `auto`, as
recommended by the [Flex processing guide](https://developers.openai.com/api/docs/guides/flex-processing).
An empty tier would inherit the provider default and could request Flex again.
A failed fallback is not replayed by the generic retry wrapper. No fallback is
applied to other HTTP errors or the ChatGPT Codex backend.

Session summaries retain the 80-token cap when reasoning is disabled. Models
that still require reasoning get 2048 tokens including reasoning; the final
summary is still trimmed by `SummaryFromContent`. Empty `response.incomplete`
results now return an error containing `incomplete_details.reason` for both JSON
and SSE responses. Partial text and tool calls remain usable.

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

T-146 extends the existing Chat Completions-compatible parent proxy instead of
adding a second Responses route. HTTP and scheduler children explicitly use
`lmstudio.NewParentProxyClient`; CLI OpenAI sessions also select it when
`A2GENT_PARENT_PROXY_URL` is set. This reuses the parent's Responses client,
provider routing, authentication and SSE adaptation without duplicating the
Responses protocol. Containers receive only the proxy token, never the parent's
provider credentials. Other providers keep their existing transports and token
limits; ordinary LM Studio/OpenRouter clients do not send private extensions.

The private JSON contract carries:

- Opaque `reasoning_items` arrays on assistant messages and response messages.
  Streaming returns the complete array in the terminal assistant delta, not as
  visible text or partial reasoning fragments. Children save/replay it through
  the same agent/session metadata used by direct sessions.
- `reasoning_effort`, `text_verbosity`, `service_tier`, `prompt_cache_key` and
  `session_id`. Nonempty request options override parent defaults. The child
  session ID takes precedence over any configured cache key, isolating its cache
  routing from the parent and other children.
- Standard `usage.prompt_tokens_details.cached_tokens` and
  `usage.completion_tokens_details.reasoning_tokens`, including the final SSE
  chunk and the non-streaming-client fallback. These are details of input/output
  usage, not additional tokens to add to the totals.

Responses stream tool indices exclude reasoning items so argument deltas merge
with final calls without duplication.

OpenAI child requests no longer inherit LM Studio's implicit 4096-token cap.
Explicit limits are preserved; the proxy accepts both `max_tokens` and
`max_completion_tokens` and maps them to Responses `max_output_tokens`.

The proxy intentionally uses full-history encrypted replay, not
`previous_response_id` continuation. Public Responses requests always ask for
encrypted reasoning, including when the parent explicitly enables response
storage, so this replay also works with that configuration. Storage remains an
explicit parent opt-in; the proxy does not enable it. Unit tests cover two tool
roundtrips with fake Responses SSE, JSON and SSE child clients, stored/stateless
parents, request options, cache isolation, credentials and detailed usage.
Real-API latency/cost improvements through the proxy have not been measured.
