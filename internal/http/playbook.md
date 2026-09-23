# Lessons learned

- Before delegating, verify that the configured agent is available to the current project. If project scoping blocks delegation, continue with an independent local verification instead of retrying the same agent.
- Credentialed catalog discovery must never let request query override the credential destination. Always use the saved provider BaseURL/default for Codex model discovery so an attacker cannot redirect API keys or OAuth tokens to an arbitrary host. Caesar may pass a `base_url` query for unsaved URL preview, but create-session uses saved config; ignoring the query in the handler is intentional.
