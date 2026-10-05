# Deterministic search and command previews

No model is used to select or collapse output.

## grep

Line searches exceeding 100 matching rows or 4000 approximate tokens (ceil(runes/4))
shows sorted per-file match counts, the first 10 matches and a narrowing hint.
`full_output=true` bypasses this summary and request-time compression, but retains
existing `max_results` (hard maximum 500), per-file emission and 500-byte line caps.
Counts cover the complete scan, independently of the emission limits. Ordering is
modification time descending, then path and line ascending. `files`/`count` modes
remain compact and do not use the new summary.

## bash

The tool returns lossless stdout followed by stderr and an explicit `Exit code:`
line (`-1` for a signal or unavailable process status). Failed results keep both
command output and status. The old destructive 50 KB cap is removed.

Above 4000 approximate tokens, transcript admission and request-time compression
use the existing session-scoped `context_retrieve` store. Previews keep the first
20 and last 40 collapsed runs, every recognized diagnostic and every stderr line.
Adjacent identical lines carry a repetition count. Near-identical matching only
ignores leading ISO timestamps on non-diagnostic, non-stderr lines; it never
normalizes error codes, source locations or other arbitrary numbers. Non-diagnostic lines above 200 runes retain their first and last 80 runes.
This bounds the 60 ordinary preview runs even when every line is very long.

Unique errors and stderr override preview budgets, including the general admission
cap. An all-error log can therefore remain large. This is intentional: a strict
cap and verbatim preservation of all diagnostics cannot both be guaranteed.
Full original output, including repeated/omitted content, remains retrievable by
hash and optional substring query, scoped to the same session.

`parallel` and command-valued `pipeline` outputs are not destructively truncated;
nested command previews retain their original wrapper output in the same store.
Pipeline intermediates remain raw so downstream tools receive the exact input.
Other tools and non-command wrapper outputs retain their existing output limits.
