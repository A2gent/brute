# Session summary reuse

When an explicit session summary prompt is saved, completed and failed runs can
reuse a previously generated LLM summary instead of making another auxiliary LLM
call. The check is asynchronous, just like summary generation.

- Jev receives exactly one `noul` question: "Does this existing summary still accurately describe the session after the latest exchange?"
- State contains only the existing LLM summary, latest user message, and final
  assistant text. Each field is head/tail capped; total state stays below 16 KiB.
  Full inputs and capped state pass `jev.SafeEgressText` before egress.
- A yes probability of at least `0.8` skips regeneration. Missing or invalid
  probabilities, classifier errors, unavailable configuration, or unsafe input
  always fall back to LLM generation.
- Regenerate on the fifth terminal run after the last successful generation,
  regardless of Jev's opinion (at most four consecutive skips). Set the saved
  setting `AAGENT_SESSION_SUMMARY_REFRESH_INTERVAL` to a positive integer to
  override this interval. `1` disables reuse; invalid values default to `5`.
- Only successfully generated and persisted LLM summaries are eligible. A
  deterministic fallback is never treated as an LLM summary. Provenance and run
  counters are process-local, so the first terminal run after server restart
  regenerates conservatively.
- Successful skips log `Session summary LLM regeneration skipped` with the
  session ID and process-wide cumulative `skip_count`.

Without a saved summary prompt, deterministic summaries behave as before and no
classifier or summary LLM calls are made.
