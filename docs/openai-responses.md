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
The ChatGPT Codex backend also receives the cache key as a `session_id` header,
like Codex CLI: measured on gpt-6-luna, `prompt_cache_key` alone gave 0 cached
tokens per turn while the header gave ~99%.

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
- `prompt_cache_retention` (public API only, never sent to the ChatGPT Codex
  backend) defaults to `24h` for models that OpenAI lists as supporting extended
  retention without a cache-write charge: `gpt-5.5`, `gpt-5.5-pro`, `gpt-5.4`,
  `gpt-5.2`, `gpt-5.1*` (base, codex, codex-max, codex-mini, chat-latest),
  `gpt-5`, `gpt-5-codex` and `gpt-4.1`, including dated snapshots. Set it to
  `in_memory` to opt out, for example for Zero Data Retention organizations
  (`gpt-5.5`/`gpt-5.5-pro` accept only `24h`). Other models, including mini/nano
  and GPT-5.6+ (which uses `prompt_cache_options.ttl`), omit the field. The
  session prompt-cache status estimates 30 minutes for `24h`, the typical
  lifetime OpenAI documents, rather than the 24-hour maximum.

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

## Prompt-cache audit (2026-10-15 UTC)

### Opt-in wire diagnostics

Set `AAGENT_LLM_CACHE_DEBUG=1` on the Brute server or a live test. Debug-level
`LLM_CACHE_REQUEST` records contain SHA-256 hashes of the **serialized wire JSON**
for tools, instructions and every input item, the model/session, a process-local
request number, the previous request number, and `first_different_item`.
`LLM_CACHE_USAGE` joins that request number to input/cached tokens and error status.
No prompt text, images, arguments or encrypted reasoning content is retained by
these diagnostics. Normal application logging has its own content policy.

- Item indices are zero-based. `-1` means identical items (or no prior request);
  `prefix_change:cold` distinguishes the latter. On an append-only request the
  first difference is the previous item count, **not** an old-item rewrite.
- `prefix_change` reports the earliest changed section: tools, instructions,
  input, or none. All item hashes are recorded even if tools/instructions differ.
- `continuation:true` identifies `previous_response_id` requests. Their delta-only
  inputs normally differ at item zero; the server-held prefix is not visible to
  this client-side comparison. Do not diagnose that as history corruption.
- State is mutex-protected and bounded to 128 recently used sessions across
  client instances. It resets on restart/eviction; empty session IDs are not
  compared. Disabled diagnostics do not hash or copy request content.
- Matching client hashes are evidence of byte stability, not a guarantee of a
  server cache hit. Routing, retention, image processing and cache granularity
  are not exposed by these hashes.

```sh
# Paid API opt-in, synthetic data only. Logs include per-turn usage/divergence
# and the persistent temporary debug-log path for inspecting all hashes.
AAGENT_OPENAI_LIVE=1 AAGENT_LLM_CACHE_DEBUG=1 \
  go test -count=1 -tags=integration ./internal/agent \
  -run '^TestOpenAIResponsesLive(MultiToolBenchmark|CodingCache)$' -v
```

### Why the original benchmark is about 66%, not 100%

The original lookup workload has **four requests**, including a cold first
request. Before any prefix fix, the stateless rerun had identical tools and
instructions on every turn, with append-only input items:

| Turn | Input | Cached | Cached/input | First different item | Explanation |
| --- | ---: | ---: | ---: | ---: | --- |
| 1 | 4,504 | 0 | 0.0% | -1 | Cold |
| 2 | 4,575 | 4,096 | 89.5% | 1 | Append |
| 3 | 4,618 | 4,096 | 88.7% | 4 | Append |
| 4 | 4,688 | 4,096 | 87.4% | 6 | Append |

Total: **12,288 / 18,385 = 66.8%**. Excluding the cold turn: **88.5%**.
Even caching every input token on turns 2-4 would only give 75.5% overall.
The remaining warm-turn uncached suffix is not explained by client prefix
changes in this run; token/block granularity and server cache behavior still
apply. In particular, identical bytes do not imply that the entire previous
request was cached. No tool ordering or history rewrite fix is justified by
this benchmark.

### Confirmed defect: lazy retrieval instructions

`internal/agent/agent.go` already registers `context_retrieve` at construction,
including when request-time compression is disabled. Its definition did **not**
appear lazily in the measured sessions. However,
`internal/agent/request_builder.go` and
`internal/contextcompress/compressor.go` added retrieval **instructions** only
after the first compressed tool result. That changed the system prefix.
Admission compression is always active, independently of `CompressToolResults`.

The fix enables retrieval guidance from the first request whenever the agent
has its compressor. Existing excerpt detection is retained. Compression,
truncation thresholds, tool selection, image normalization and reasoning replay
are unchanged. The guidance costs 35 input tokens in the initial measured
request, but avoids the confirmed later prefix reset.

The longer stateless fixture runs seven sequential tools over eight requests:
read synthetic code, emit a large failing test log, edit, retest, capture a
synthetic 64x64 PNG, reread and retest. It uses the real agent loop, admission
compression (512-token cap), session persistence, encrypted reasoning and
screenshot normalization. Fixture tools do not edit or execute host files.

| Turn | Preceding result | Before input/cached | Before ratio | After input/cached | After ratio |
| --- | --- | ---: | ---: | ---: | ---: |
| 1 | None | 4,778 / 0 | 0.0% | 4,813 / 0 | 0.0% |
| 2 | Read | 4,865 / 4,608 | 94.7% | 4,903 / 4,608 | 94.0% |
| 3 | Compressed test log | 5,398 / 0 | 0.0% | 5,393 / 4,608 | 85.4% |
| 4 | Edit | 5,449 / 5,120 | 94.0% | 5,434 / 5,120 | 94.2% |
| 5 | Passing test | 5,497 / 5,120 | 93.1% | 5,473 / 5,120 | 93.6% |
| 6 | Screenshot | 5,483 / 0 | 0.0% | 5,485 / 0 | 0.0% |
| 7 | Reread | 5,577 / 5,120 | 91.8% | 5,566 / 5,120 | 92.0% |
| 8 | Passing test | 5,616 / 5,120 | 91.2% | 5,605 / 5,120 | 91.3% |

Before: tools stayed identical, but instructions changed at turn 3. The first
input differences at turns 2-8 were `1,4,7,10,13,17,20`, all appends.
After: tools **and instructions** stayed identical; differences were
`1,4,6,8,10,13,16`, again all appends. Different reasoning output lengths/item
counts are new tail content, not changes to replayed encrypted items.

| Workload | Phase | Input | Cached | Total ratio | Ratio excluding cold turn | Latency |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Lookup, stateless | Before | 18,385 | 12,288 | 66.8% | 88.5% | 4.337 s |
| Lookup, stateless | After | 18,529 | 12,288 | 66.3% | 87.8% | 4.770 s |
| Lookup, stored | Before | 18,397 | 12,288 | 66.8% | 88.4% | 4.670 s |
| Lookup, stored | After | 18,521 | 12,288 | 66.3% | 87.9% | 5.091 s |
| Coding, stateless | Before | 42,663 | 25,088 | 58.8% | 66.2% | 11.464 s |
| Coding, stateless | After | 42,672 | 29,696 | 69.6% | 78.4% | 8.021 s |

All rows use `gpt-5.4-mini`, low reasoning/verbosity, session-ID cache keys, and
one valid run per phase/workload. The first after-run lookup sessions repeated
calls (four stateless, five stored) despite returning `89`; their exact-three-call
assertions failed. They were retained as failed measurements and excluded from
this comparable table. A repeat without prompt changes passed both variants.
These are diagnostic observations, not a statistically controlled speed/cost
claim. Model output and cache routing vary.

### Other suspects and limits

- `internal/tools/manager.go` already sorts definitions by name. Go's
  `encoding/json` sorts string map keys, including nested `InputSchema` maps.
  Repeated wire-level regression requests recreate these schemas and verify
  identical bytes. No additional sorting was added.
- `enable_tools` deliberately expands session tool groups. MCP/integration/tool
  registrations can also change the available definitions. These are actual
  capability changes, not nondeterministic map order; they can reset a prefix.
  The measured fixtures keep capabilities fixed. They do not prove that an
  arbitrary live integration's schema/description is stable.
- HTTP system prompts use the persisted session snapshot in
  `internal/http/system_prompt.go`; the timestamp fix remains intact. The
  observed dynamic instruction was retrieval guidance, not task progress,
  memory or a new timestamp. Existing HTTP prompt-stability tests are retained.
- In both measured stateless sessions, all previously sent input item hashes
  remained identical, including encrypted reasoning, compressed results and
  screenshots. `internal/agent/cache_prefix_test.go` also checks actual wire
  tools/instructions and input prefixes across repeated builds, new results,
  admission-only/request-time compression and SQLite reloads. It checks opaque
  `encrypted_content` verbatim. Reasoning still precedes function calls.
- Compaction/provider transitions intentionally replace context; the fixtures
  do not reach those boundaries. No compaction/history policy was changed.
- The first screenshot was followed by **zero cached tokens in both runs**, but
  tools, instructions and every prior item were byte-identical. This local
  instrumentation cannot establish why OpenAI did not reuse that prefix. Image
  rewriting/routing changes would be speculative, so screenshot handling was
  left unchanged. Follow up with a controlled text/image cache experiment.

## Remaining proxy limitation

Child/container calls using `A2GENT_PARENT_PROXY_URL` still use the existing
Chat Completions-compatible parent proxy. It does not carry encrypted reasoning
items or Responses continuation state. Direct-provider optimization does not
remove that limitation. Follow-up task T-146 covers the proxy contract and its
multi-tool regression test.
