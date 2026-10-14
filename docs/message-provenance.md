# Immutable message provenance

Contract for the parallel provider-transition implementation:

- Assistant messages and assistant/runtime stream events carry optional `provenance: { provider: string, model: string }`.
- `provider` is the concrete provider reference, including named instance (`anthropic:work`), never `automatic_router` or `fallback_chain:*`.
- The producing request captures provenance. Session provider selection, routing traces and transitions must never backfill or rewrite historical provenance.
- Brute persists this object in message metadata under `llm_provenance`, exposing it as top-level `provenance` in message JSON/API. No database migration is required.
- Missing or untrustworthy provenance means unknown. Legacy `llm_provider` / `llm_model` timing metadata is not authoritative (historically it could describe an aggregate).
- Wrappers preserve inner concrete provenance and supply it only when absent. Streaming provenance is set before forwarding content/reasoning/native-tool events.
- Compaction preserves provenance on retained/archived transcript messages. A generated summary is a new message, never a reason to relabel old messages.
- Transition policy, confirmation and scheduling are outside this contract. Transition implementations should rebuild the agent with the target concrete provider/model or use a provenance-aware routing wrapper.

User decision: switching A to B changes only new messages, including fallback within a request. Historical A messages retain A during streaming, snapshot reconciliation, persisted reload and compaction.

## Transition integration

`provider-transitions.md` uses `source` / `target` endpoints with the same `provider` / `model` members as provenance. Its transition state is independent of message provenance. The new transition summarizer must stamp its newly generated summary from `Compaction.Summarizer` (or concrete response provenance), not from the source session provider; retained messages must be copied unchanged. Ordinary compaction and the agent continuation path already use the producer wrapper here.

The parent OpenAI-compatible proxy also forwards optional top-level `provenance` in responses and chunks. Compatible clients propagate this object; transport-reported `model` overrides a configured model alias without changing the named provider reference. Missing model information is left empty rather than guessed from another provider.
