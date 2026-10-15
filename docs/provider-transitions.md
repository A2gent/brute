# Durable provider transitions (v1)

## Decisions and scope

Manual provider changes and fallback default to explicit confirmation before compaction or any target request. Approval compacts context and continues **in the same session**. Composer selections remain local drafts until the user requests the switch or submits a message. A linked continuation is opt-in and is not a delegated child agent. Policy is captured at transition creation; later settings changes do not alter pending consent.

Routing modes (`mode`):
- `compact_auto`: summarize, commit target, rebuild request, continue.
- `switch_auto`: keep active context, invalidate provider state, rebuild, continue. Pause if the estimated request does not fit the target window.
- `confirm` (default): persist `awaiting_confirmation` before ANY target request (including summarization). Approve performs compact_auto; reject leaves provider and history unchanged.
- `linked_session`: compact and create an explicitly linked continuation, leaving the source transcript/provider intact. Never use delegated agent parentage.

Config: `provider_transition_mode`. Settings override: `A2GENT_PROVIDER_TRANSITION_MODE`. Explicit API `mode` overrides settings/config. Unknown values are rejected (runtime misconfiguration pauses rather than silently bypassing consent).

## API

All paths are under the existing API prefix. Responses contain the complete transition snapshot.

- `GET /sessions/{id}/provider-transitions`: `{ "version": 1, "transitions": [...] }` (oldest first).
- `POST /sessions/{id}/provider-transitions`: `{ "id": "client-generated-id", "provider": "openai", "model": "gpt-5", "mode": "compact_auto" }`. ID is required for retry deduplication. Direct concrete targets only; aggregates must be resolved before requesting a manual transition. Returns 202, or 200 on an identical retry. Conflicting reuse returns 409.
- `POST /sessions/{id}/provider-transitions/{transitionID}/decision`: `{ "decision": "approve" | "reject" }`. Repeating the same decision is idempotent (200); reversing it is 409. Approval schedules continuation without adding another user prompt.
- `POST /sessions/{id}/provider-transitions/{transitionID}/resume`: explicitly retry a paused transition. Interrupted continuation is NOT automatically replayed: native provider tools may have executed. Resume is only safe before continuation submission; otherwise returns 409.
- Existing `PUT /sessions/{id}/provider` uses the same mechanism, with optional `transition_id` and `mode`; never edits provider metadata behind an active run.

Manual changes during a run return 409: cancel/wait for the safe boundary first. Confirmation actions never run alongside an active agent. A paused transition blocks ordinary chat from bypassing it.

## Persistence and events

Session metadata `provider_transitions` is an append-only list of versioned records. Fields: `version`, `id`, `source`, `target` (provider/model), `cause` (`manual`/`fallback`), `mode`, `phase`, `confirmation`, `context_strategy`, `compaction`, `created_at`, `updated_at`, `revision`, `error`, optional `fallback_index`, `chain_provider`, `linked_session_id`. Compaction records outcome, summarizer identity, measured usage, before/after input estimates and target window when available. When Jev admission gating is configured, large history tool outputs are relevance-scored before summarization and low-scoring ones are replaced by placeholders (archived transcript unchanged); `jev_used`, `jev_scored`, `jev_dropped` and `jev_dropped_bytes` record this. Any Jev failure summarizes the unfiltered history. Regular context compaction stores the same keys in the summary message metadata. Estimates are conservative byte-based heuristics, NOT provider billing.

Phases: `requested`, `awaiting_confirmation`, `compacting`, `ready`, `continuing`, `completed`, `rejected`, `paused`, `linked`. Confirmation: `not_required`, `pending`, `approved`, `rejected`. Context: `compact` or `current`. Compaction outcome: `not_started`, `succeeded`, `failed`, `skipped`.

SSE `provider_transition` carries `{ "type": "provider_transition", "transition": <snapshot> }` after successful persistence. Deduplicate by `(id, revision)`. Events are invalidation hints; GET/session metadata is authoritative after reconnect. Existing `provider_trace` remains diagnostic, not a commit or consent event. Fixtures live in `docs/fixtures/provider-transitions.json`.

## Safety, summarizer, recovery

Fallback returns a typed boundary signal before contacting the next provider. The agent persists intent, checks consent, summarizes, commits, and builds a NEW request. Summarization uses the concrete target client without fallback/retry wrappers, without tools, session cursor or previous-response/cache state. Thus a failed source is never required. No target egress occurs before approval. Large history is chunked to fit the target window; summary reduction is bounded. If the target cannot summarize, pause with the original active context intact. No recursive provider fallback during summarization.

All history stays archived. Summary activation, cursor invalidation, provider selection and ready state are saved together. Pending user messages are retained, and no tool calls are re-executed by the transition. Whole call/result groups are summarized rather than split. Empty/error/oversized summaries or save failures roll back context changes and explicitly pause/report. Summarization usage is reported when available.

Run ownership is session-scoped. Cancellation and unsafe-retry errors never trigger a new target request. On restart, requested/approved/ready work can be explicitly resumed. An interrupted compacting phase pauses and can be explicitly retried; an interrupted continuing phase pauses for human inspection, not automatic replay. Durable exactly-once external tool execution cannot be inferred after a process crash; fail closed rather than promise it. Duplicate approval never starts another run.

## Durable Caesar timeline extension (v1, additive)

New records include `timeline: { after_message_id, message_count, sequence }`, captured by the first persistence and immutable on later revisions. The anchor is the last original transcript message at intent creation, before a summary/continuation/target response. Multiple transitions at the same boundary use increasing `sequence`. Metadata notices are never conversation messages and must not be added to LLM requests. Old records without anchors can only use best-effort creation-time placement; do not invent anchors from current provider selection.

`source_outcome`: `failed` or `skipped_usage_limit`. `retry_count` is optional and counts observed retries (attempts minus one), not the configured maximum; zero is meaningful. `target_outcome`: `selected`, `succeeded`, `failed`, `skipped_usage_limit`. Only a persisted successful nonempty response/tool-call batch sets `succeeded`; selection, approval, compaction and commit do not. A `completed` transition may close an unsuccessful target before a subsequent transition; do not treat the phase alone as recovery. Legacy completed records without an outcome have unknown response status.

Reconnect SSE `session_snapshot` includes `provider_transitions`; merge records with live SSE/GET by ID and highest revision. Never let a stale snapshot erase a newer revision. Pagination uses anchors into archived original messages; notices outside the loaded page stay in Router history until their anchored page loads. UI uses the shared persisted IDs for both inline notices and Router history. Confirmation actions remain solely in `ProviderTransitionPanel` and use the saved Routing policy; notices do not add alternative approval controls. Historical avatars use immutable message provenance, not transition endpoints. Transition summary producer provenance is stamped on the newly generated summary only.

Compaction before/after values are byte-based conservative input heuristics with different scopes (summary source vs rebuilt request), not measured token counts or exact savings. Label them explicitly. The context is either the unchanged prepared conversation or a summary plus retained recent messages. Provider caches are not transferred.
