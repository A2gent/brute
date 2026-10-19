# Jev classify tool

`classify` exposes TypeSafe System One (`POST /v1/systemone`) independently of the existing LLM router. The router's request and retry behavior is unchanged.

The tool is available when a Jev provider API key, `TYPESAFE_API_KEY`, or an enabled `jev` integration with `config.api_key` exists, in that precedence order. Provider/environment credentials use provider model/base URL; integration credentials use their own optional `model` and `base_url`. Session managers refresh registration so credential changes take effect on subsequent sessions/turns. Existing per-session disabled-tool and sub-agent allowlist filtering still applies.

Example:

```json
{"input":"Please add unit tests","question":"What is the requested work?","type":"choice","criteria":{"code":"Code changes","docs":"Documentation"}}
```

Use exactly one of `input` and `path`. Relative paths resolve against the session working directory. Both sources are capped at 64 KiB of UTF-8 state, preserving head and tail with a `[truncated]` marker. Large regular files are read only at the retained ends. Requests have a 30-second context timeout and inherit earlier caller cancellation.

- `choice`: criteria is a map of 2-10 option keys to descriptions.
- `score`: criteria is a map with consecutive numeric keys `0` through `N`, describing 2-10 ordered low-to-high levels. It is sent as an ordered criteria array, as required by the API.
- `noul`: optional `true`/`false` criteria descriptions. Answer is the probability of yes, not a boolean.

Output always has `answer`, `confidence`, and `probabilities`. Choice answers are strings; score/noul answers are numbers, including zero. Noul has no separate confidence or probabilities in the API, so those fields are `null`, not invented metrics.

## Context limits

Jev limits are measured in tokens: 64k per request and 32k per question (state plus one question), see https://docs.typesafe.ai/models#context-length. The 64 KiB tool cap is a local byte policy and can still exceed 32k tokens for dense non-English text; the API then returns `400 max_tokens_exceeded`.

The automatic router caps the routed prompt at 32 KiB (head and tail) and, on `max_tokens_exceeded`, retries with the prompt halved. Routing is sticky: only the first prompt of a session is classified; follow-ups reuse the stored route until the user switches provider (which clears it) or the routed rule is removed from config. Images from the latest user message are forwarded to Jev only (resized JPEG, `TypeSafe-Danger-Zone-Images` header) and dropped if Jev rejects them.

References: https://docs.typesafe.ai/primitives/choice, https://docs.typesafe.ai/primitives/score, https://docs.typesafe.ai/primitives/noul.
