# Jev Baseline: Where Context Tokens Actually Go

Baseline measurement taken before any Jev-style context gating, to decide whether gating is
worth building and which tools it should target.

- **Script**: `brute/scripts/jev_baseline.py` (read-only, re-runnable)
- **Corpus**: the local session archive `~/.local/share/aagent/sessions/*.jsonl` (9,712 files,
  3.8 GB, written by `internal/session/jsonl_writer.go`) plus the `messages` table of
  `~/.local/share/aagent/aagent.db`
- **Filter**: sessions with >= 10 tool-result messages (3,595 kept, 6,268 skipped as
  aborted/chat-only runs)
- **Token unit**: `ceil(runes/4)` on trimmed text - the same approximation brute itself uses in
  `estimateTokensApprox` (`internal/http/instruction_blocks.go:173`)

Reproduce with:

```bash
python3 brute/scripts/jev_baseline.py --selftest                                   # verify the accounting rules
python3 brute/scripts/jev_baseline.py --limit 0 --db ~/.local/share/aagent/aagent.db
```

The full run takes ~55s. `--selftest` checks the accounting on hand-built sessions with known
answers: resend weighting, `parallel` unwrapping, duplicate-read detection, and in particular that
an identifier appearing only *before* a tool result is not miscounted as reuse.

The archive stores **raw, pre-compression** tool results: compression runs later, at request-build
time (`internal/agent/request_builder.go:85`), and the persisted message keeps the original. So
these numbers are a clean "no gating" baseline, not a measurement of the current CCR behaviour.

## Headline numbers

| Metric | Value |
|---|---:|
| Sessions analysed | 3,595 |
| Messages | 259,423 |
| Tool calls (after unwrapping `parallel`/`pipeline`) | 296,298 |
| Total transcript context | 355,163,816 tokens |
| Resend-weighted spend | 10,191,004,469 tokens |
| Average resend factor | **28.7x** |
| Tool-result tokens per session | median 63,050 / p95 217,356 / max 5,199,649 |
| Image payloads (excluded from token counts) | 994.8 MB of base64 |

"Resend-weighted spend" multiplies every message by the number of later assistant turns that
re-ship it. It is the metric that matters for cost: a payload admitted on turn 3 of a 40-turn
session is paid for ~37 times.

## Context composition

| Bucket | Tokens | Share of transcript | Resend-weighted tokens | Share of weighted spend |
|---|---:|---:|---:|---:|
| tool_result | 307,250,185 | 86.5% | 8,839,800,705 | 86.7% |
| tool_call_args | 36,136,668 | 10.2% | 995,392,016 | 9.8% |
| assistant_text | 6,230,983 | 1.8% | 121,693,144 | 1.2% |
| user_text | 5,545,980 | 1.6% | 234,118,604 | 2.3% |

Everything the human and the model actually *say* is 3.4% of context. Tool I/O is 96.7%.

## Per-tool result size

`parallel` and `pipeline` results are unwrapped into their inner steps, so a `read` issued inside
`parallel` is attributed to `read`.

| Tool | Calls | Total tokens | Avg | p50 | p95 | Max | % of tool output | % of transcript | % of weighted spend |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| read | 86,437 | 130,657,955 | 1,512 | 933 | 4,963 | 48,555 | 42.5% | 36.8% | 40.8% |
| bash | 93,552 | 82,865,743 | 886 | 221 | 3,919 | 12,806 | 27.0% | 23.3% | 19.2% |
| grep | 31,952 | 36,814,292 | 1,152 | 338 | 4,760 | 49,783 | 12.0% | 10.4% | 12.2% |
| tasks | 15,704 | 17,923,385 | 1,141 | 176 | 8,503 | 46,714 | 5.8% | 5.0% | 4.5% |
| fetch_url | 2,048 | 7,332,827 | 3,580 | 1,048 | 7,613 | 1,112,137 | 2.4% | 2.1% | 2.2% |
| browser_chrome | 2,179 | 6,005,046 | 2,756 | 22 | 1,044 | 5,169,239 | 2.0% | 1.7% | 0.3% |
| chrome_extension | 1,010 | 4,702,353 | 4,656 | 392 | 6,962 | 1,267,385 | 1.5% | 1.3% | 0.6% |
| find_files | 12,034 | 3,089,791 | 257 | 47 | 1,171 | 2,711 | 1.0% | 0.9% | 1.1% |
| delegate_to_agent | 1,977 | 929,781 | 470 | 404 | 1,143 | 2,006 | 0.3% | 0.3% | 0.2% |
| glob | 752 | 869,879 | 1,157 | 47 | 6,713 | 30,416 | 0.3% | 0.2% | 0.2% |
| project_session_history | 429 | 846,890 | 1,974 | 95 | 13,545 | 44,556 | 0.3% | 0.2% | 0.4% |
| tavily_search | 591 | 684,217 | 1,158 | 1,256 | 1,832 | 3,371 | 0.2% | 0.2% | 0.2% |
| edit | 19,951 | 405,789 | 20 | 21 | 27 | 51 | 0.1% | 0.1% | 0.1% |
| exa_search | 558 | 395,844 | 709 | 506 | 1,718 | 2,225 | 0.1% | 0.1% | 0.1% |
| brave_search_query | 819 | 348,146 | 425 | 97 | 1,345 | 2,564 | 0.1% | 0.1% | 0.1% |
| suggest_session | 1,295 | 345,913 | 267 | 248 | 441 | 754 | 0.1% | 0.1% | 0.0% |
| content_search | 1,419 | 256,013 | 180 | 31 | 1,077 | 4,522 | 0.1% | 0.1% | 0.1% |
| context_retrieve | 1,222 | 253,832 | 208 | 22 | 982 | 8,763 | 0.1% | 0.1% | 0.1% |

`read` + `bash` + `grep` = **81.5%** of all tool output. The remaining ~30 tools share 18.5%.

## Never-referenced estimate

A result line counts as "echoed later" if any distinctive identifier on it (>= 4 chars, not a
stopword) reappears in a *later user message, assistant message, or assistant tool-call argument*.
Other tool results are deliberately not counted - one tool quoting another proves nothing about
the model having used the text. Lines with no scoreable identifier (pure punctuation/numbers) are
excluded from the denominator.

| Tool | Scoreable tokens | Never echoed later | Share | Calls w/ zero reuse | Error-result tokens |
|---|---:|---:|---:|---:|---:|
| read | 119,139,196 | 33,636,010 | 28.2% | 1,145 / 86,437 | 35,902 |
| bash | 80,119,705 | 21,724,419 | 27.1% | 3,263 / 93,552 | 1,098,777 |
| grep | 36,908,555 | 3,154,197 | 8.5% | 4,474 / 31,952 | 2,290 |
| tasks | 17,941,049 | 286,058 | 1.6% | 303 / 15,704 | 1,074 |
| fetch_url | 6,633,598 | 2,697,209 | 40.7% | 277 / 2,048 | 6,194 |
| browser_chrome | 5,914,865 | 5,448,582 | 92.1% | 283 / 2,179 | 2,920 |
| chrome_extension | 4,435,169 | 3,372,792 | 76.0% | 17 / 1,010 | 2,128 |
| find_files | 3,119,046 | 825,029 | 26.5% | 2,208 / 12,034 | 0 |
| delegate_to_agent | 919,479 | 136,795 | 14.9% | 50 / 1,977 | 14,575 |
| glob | 875,573 | 114,396 | 13.1% | 58 / 752 | 7 |
| project_session_history | 839,678 | 34,871 | 4.2% | 54 / 429 | 110 |
| tavily_search | 664,046 | 155,971 | 23.5% | 15 / 591 | 275 |
| edit | 405,789 | 2,404 | 0.6% | 197 / 19,951 | 13,719 |
| exa_search | 389,808 | 112,549 | 28.9% | 84 / 558 | 2,290 |
| brave_search_query | 347,713 | 53,324 | 15.3% | 45 / 819 | 11,317 |
| suggest_session | 332,076 | 5,394 | 1.6% | 7 / 1,295 | 0 |
| content_search | 256,027 | 15,615 | 6.1% | 131 / 1,419 | 4,271 |
| context_retrieve | 253,169 | 62,550 | 24.7% | 145 / 1,222 | 247 |

Two biases pull in opposite directions, so this is **not** a strict bound either way:

- **Understates waste**: the rule is lenient. A single recurring identifier marks a whole line as
  used, and an identifier that merely happens to recur (a common function or variable name) counts
  as reuse even when the model never looked at that line.
- **Overstates waste**: use is not always lexical. The model can read a file, conclude "this is
  irrelevant" or "the config is already correct", and act on that without restating any token.

Empirically the first effect dominates for bulk-content tools (`read`, `bash`), where most lines
carry identifiers that recur somewhere later, and the second dominates for small results. Treat
this column as a **ranking signal between tools**, not as an exact waste figure. The
whole-file read signals in the next section are the load-bearing evidence, because they are
derived from call arguments rather than from this heuristic.

## Whole-file read waste

Two signals that do not depend on the lexical heuristic at all, computed from `read` call
arguments:

- `read` calls with a resolvable path: **86,082** (130,340,727 tokens)
- reads whose file is never mentioned again by the model: **24,232** calls,
  **30,300,518** tokens (23.2% of read tokens)
- repeat reads of a path already read in the same session: **35,069** calls,
  **47,873,253** tokens (36.7% of read tokens)
- **union of the two, counted once per call: 54,152 calls, 71,757,398 tokens (55.1% of read
  tokens, 20.2% of all transcript context)**
- `parallel`/`pipeline` wrapper framing overhead: 10,882,552 tokens (3.1% of transcript)

## Conclusions

### 1. Gating only matters for `read`, `bash` and `grep` - and it must happen at admission, not at compaction

Tool results are 86.5% of transcript context and 86.7% of resend-weighted spend, and three tools
carry 81.5% of that. The long tail of ~30 integrations is 18.5% and not worth gating logic. The
decisive multiplier is the **28.7x average resend factor**: one 5k-token result admitted early
costs ~140k tokens over the session. That is why gating has to decide *before* a payload enters the
transcript. Retro-active compaction only stops the bleeding from the turn it runs; admission
gating never starts it. It also sets the ceiling on the whole project: perfect gating of these
three tools addresses ~70% of weighted spend, and nothing else comes close.

### 2. Over half of `read` output is provably redundant without any semantic judgement

55.1% of read tokens (71.8M, or 20.2% of all context) are either a **re-read of a file already
read in the same session** (36.7%) or a read of a file **the model never mentions again** (23.2%).
The first class is pure duplication and is decidable with a per-session path -> content-hash cache:
re-serve a `[unchanged, see turn N]` stub instead of the file body. That alone is ~13% of total
context, needs no model in the loop, and cannot lose information. The second class needs a cheap
relevance check, but note that today's CCR explicitly **excludes `read` from compression**
(`internal/contextcompress/compressor.go:338`, documented in `docs/context-compression-mvp.md`
as "`read`, `write`, `edit` are never compressed"). The single
largest consumer of context is currently the one tool with no gating at all - that exemption is
the main gap Jev should close, with exactness preserved via the existing `context_retrieve` path.

### 3. The tail risk is per-call size, not per-call average - cap first, summarise second

`browser_chrome`, `chrome_extension` and `fetch_url` are only 5.9% of tool output but have brutal
distributions: `browser_chrome` has a p50 of 22 tokens and a max of 5,169,239, and `fetch_url`
peaks at 1,112,137. One such call exceeds any production context window and torches the session.
Their never-echoed shares are also the worst measured (92.1%, 76.0%, 40.7%) - they are mostly dumped
and ignored. A hard per-call admission cap with retrieval-on-demand is a small, high-value change,
and it is a different mechanism from the proportional trimming that `read`/`bash` need. Secondary
targets in the same vein: `bash` carries 1.1M tokens of pure error text and 27.1% never-echoed
output, and `grep`'s 4,474 zero-reuse calls out of 31,952 point at over-broad patterns better
answered by a count-first response than by a full match dump.

## Caveats

- Token counts are `ceil(chars/4)`, matching brute's own estimator rather than a real BPE
  tokenizer. Code and JSON tokenize denser than prose, so absolute totals are rough (order
  ±20%); the *relative* per-tool shares are far more reliable than the absolute figures.
- Resend-weighted spend assumes no summarisation or compaction ran. Real sessions did compress
  search-tool output at request time, so 10.2B is an upper bound on naive cost, and the right way
  to read it is as the size of the prize, not as a past invoice.
- Base64 image payloads (994.8 MB) are excluded from all token counts; vision token cost is not
  modelled here.
- Sessions with < 10 tool results were skipped, which biases the corpus toward real agentic work
  (that is intentional) and away from short chat sessions.
- The corpus is one developer's local archive across several projects, so tool mix reflects those
  habits; `tasks` at 5.0% of context, for example, is unlikely to generalise.
