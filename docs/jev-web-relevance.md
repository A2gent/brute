# Optional Jev web relevance filtering

## Configuration

Disabled by default. Set `tools.web_relevance` in Brute's JSON configuration:

```json
{
  "tools": {
    "web_relevance": {
      "enabled": true,
      "threshold": 2,
      "top_n": 0,
      "min_confidence": 0.85,
      "max_page_bytes": 65536
    }
  }
}
```

Uses the existing classify credential resolution: configured Jev provider, `TYPESAFE_API_KEY`, then an enabled Jev integration. Session/project managers refresh credentials and policy without stacking wrappers. Missing credentials or failed credential inventory disables classifier egress, retaining source output.

Targets `fetch_url`, `tavily_search`, `exa_search`, and `brave_search_query`. With the flag off, tool schemas, descriptions and results are unchanged. With it on, each accepts:

- `task_context`: the task/goal against which candidates are scored. Searches default to their `query`; fetch requires an explicit context.
- `filter_results`: explicit `false` bypasses Jev and the page cap to recover original content. Omission/true uses configured policy, not an override of the global flag.

`threshold` keeps scores >= the threshold on an ordered 0-4 scale (irrelevant, tangential, useful, highly relevant, directly answers). Zero/invalid threshold defaults to 2. Positive `top_n` selects that many highest-scoring candidates instead of applying the threshold; ties keep original order. Every answer must meet `min_confidence` (default .85); invalid/zero confidence configuration defaults to .85. `max_page_bytes` defaults to 64 KiB; invalid or larger values use 64 KiB.

## Processing and recovery

Pages split at ATX Markdown headings outside fenced code, with long/heading-free sections split into <=4 KiB UTF-8 chunks. Candidates are scored in batches of <=10; classifier state is bounded to roughly 46 KiB. All candidate bytes reach Jev: long search items and task contexts over 4 KiB bypass classification rather than trusting partial previews. Native search boundaries are captured before formatting, preventing snippets from forging result identities. Tavily's optional generated answer is a separate candidate, not an unfiltered bypass.

Dropped items/sections always appear in a compact manifest with their original titles, URLs and scores. Retained content is original text, not generated replacements. All-drop emits the manifest rather than pretending there were no results. Recover with the same call and `filter_results:false`; repeat search results can change, so this is not a cached archive.

Malformed/missing/out-of-range answers, low confidence, classifier errors and deadline/cancellation return the source result. The classification budget is 30 seconds across all batches. Enabled fetch fallback is full content capped to `max_page_bytes`, UTF-8 safe, with an explicit cap marker. Pages exceeding the cap bypass classification entirely so the omitted tail is not implicitly judged irrelevant. A disabled policy or explicit bypass preserves native output. Native tool errors remain errors. No-context fetches do not contact Jev, but the enabled page cap still applies.

## Security and privacy

Only task context and candidate text are sent, never integration objects, headers, arbitrary raw tool parameters or credential lists. A local guard checks complete inputs before truncation. It bypasses Jev for known provider/integration/environment credentials (including OAuth, sensitive secrets and credential-bearing provider environment overrides), credential patterns, private-key blocks, bearer strings, credential-bearing/queried URLs, private/local literal URLs and secret paths. Failed credential inventory installs a no-client wrapper rather than leaving stale credentials active. Suspect content remains in the local source result, not in classifier requests.

This is conservative best-effort detection, **not a DLP guarantee for arbitrary unlabeled unknown secrets**. Public-looking domain names are not DNS-checked and may resolve privately. Do not enable this feature for confidential/internal sources without an independent disclosure policy. Task context and web content are untrusted data; the classifier can only rank retention, not fetch URLs or execute actions. The wrapper is not an SSRF defense and does not change underlying fetch permissions. Manifests themselves can contain sensitive local titles/URLs.

## Reproducible token measurements

Run from `brute/`:

```sh
./scripts/jev_web_relevance_benchmark.sh
go test ./internal/tools/integrationtools ./internal/http -run '^TestWebRelevance' -count=1
```

Corpus: three **synthetic local sample pages**, clearly labeled in `internal/tools/integrationtools/testdata/web_relevance`. They are not public downloads. The harness invokes the **production wrapper** with fake source tools and a local fake Jev HTTP server. Known relevant sections/results receive score 4, others 0, confidence .99. No public network or credentials. Search cases model native formats, including Exa's pre-existing 500-byte content cap.

Token estimate: `ceil(trimmed Unicode rune count / 4)`, matching the existing Brute approximation. BEFORE is original tool-result text; AFTER includes retained text, the entire drop manifest and recovery instruction. Tool-call arguments and Jev request/response tokens are excluded. These numbers measure admitted main-model result size, not billed tokens, real Jev accuracy or total end-to-end cost. Real classification adds cost and latency; a manifest may increase very small results. Timing includes fake HTTP/server setup and is not production latency.

Measured 2026-10-05, darwin/arm64, production wrapper, `-benchtime=100x`:

| Sample / tool | Before approx tokens | After approx tokens | Saved | Reduction |
|---|---:|---:|---:|---:|
| Cancellation / fetch | 553 | 346 | 207 | 37.4% |
| Cancellation / Tavily | 800 | 343 | 457 | 57.1% |
| Cancellation / Exa | 467 | 231 | 236 | 50.5% |
| Cancellation / Brave | 801 | 343 | 458 | 57.2% |
| Cache / fetch | 573 | 351 | 222 | 38.7% |
| Cache / Tavily | 805 | 349 | 456 | 56.6% |
| Cache / Exa | 473 | 237 | 236 | 49.9% |
| Cache / Brave | 806 | 350 | 456 | 56.6% |
| SQLite / fetch | 573 | 355 | 218 | 38.0% |
| SQLite / Tavily | 801 | 343 | 458 | 57.2% |
| SQLite / Exa | 469 | 233 | 236 | 50.3% |
| SQLite / Brave | 802 | 343 | 459 | 57.2% |
| **Total** | **7,923** | **3,824** | **4,099** | **51.7%** |

Tests cover selection/top-N, default-off and explicit bypass, query fallback, malformed/API-error/deadline/low-confidence fallback, UTF-8 chunk coverage and capped pages, secret exclusion, credentials removal/inventory failure, native-boundary capture, and token-estimator boundaries.
