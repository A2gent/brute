- Run Git commands inside the component repository, and pass directories (not file paths) to indexed content_search. Use grep with include for a single-file search.
# Lessons learned

- Before delegating, verify that the configured agent is available to the current project. If project scoping blocks delegation, continue with an independent local verification instead of retrying the same agent.
- Credentialed catalog discovery must never let request query override the credential destination. Always use the saved provider BaseURL/default for Codex model discovery so an attacker cannot redirect API keys or OAuth tokens to an arbitrary host. Caesar may pass a `base_url` query for unsaved URL preview, but create-session uses saved config; ignoring the query in the handler is intentional.
- **HTML preview and ServeFile** - `http.ServeFile` returns 301 to `./` for any URL ending in `/index.html`. Serve project HTML with `http.ServeContent` so the document URL stays on the file and relative JS/CSS/models resolve. A query-param raw URL cannot be the iframe base for the same reason.
- Tests that bind fixed localhost ports can collide across concurrent agent sessions even when only one test package appears to fail. Confirm port ownership and serialize the owning test/commit hook; do not modify unrelated fixtures to mask the collision.
