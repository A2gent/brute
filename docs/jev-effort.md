# Session-sticky Jev reasoning effort

Enable in Brute's config.json (disabled by default):

```json
{
  "tools": {
    "jev_effort": { "enabled": true }
  }
}
```

Configure the Jev provider/integration or the existing System One backend as usual. No automatic-router rules are needed.

For fixed OpenAI and OpenAI Codex providers with supported reasoning models, the first session run without an explicit `metadata.reasoning_effort` submits one score question over the routed prompt. State uses the router's 32 KiB UTF-8 head/tail preview. The full prompt must pass the existing Jev egress guard before anything is sent.

| Level | Request | Effort |
| --- | --- | --- |
| 0 | Trivial lookup or chat | low |
| 1 | Small edit | low |
| 2 | Multi-file change | medium |
| 3 | Design or debugging | high |

Model-specific minimums still apply (for example, GPT-5 Pro requires high). Scores below 0.85 confidence, missing/invalid scores, unavailable credentials, unsafe prompts, and classifier failures preserve the configured provider default. The request has a 30-second deadline and is not retried.

The effective effort and explanation are persisted as `metadata.jev_reasoning_effort` and `metadata.jev_effort_reason`, including fail-open defaults. Later runs reuse them without classification, even if the prompt or provider default changes. Explicit `metadata.reasoning_effort` always takes precedence. To request a fresh automatic decision, clear these two Jev metadata keys and any explicit effort. Auxiliary summary generation does not initialize a decision.

Decisions use the existing `router_decision` event with `routed_rule: "jev_effort"`, and include the selected effort and confidence in `routed_reason`. Caesar displays them in the session panel. Automatic-router sessions retain their existing rules and effort behavior.
