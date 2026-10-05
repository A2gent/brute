# Offline web-relevance corpus

These three markdown files are **synthetic local samples**, authored for this benchmark. They are not downloads, public-page snapshots, or verbatim upstream documentation. The reserved `https://example.test/` URLs are citation identities only; never fetch them.

Each page has a title, provenance paragraph, navigation, two useful technical sections, an irrelevant promotional section, and a footer. The task-specific useful sections and fake scores are fixed in `web_relevance_benchmark_test.go`, not inferred by a real classifier.

- `request_cancellation.md`: propagate cancellation and deadlines to Go HTTP requests.
- `cache_revalidation.md`: explain conditional HTTP requests and private-cache constraints.
- `sqlite_transactions.md`: explain atomic commits, WAL, and durability.

Search fixtures are constructed deterministically from these pages in the native Tavily/Exa/Brave output format: numbered titles, `URL:`, and snippet/content lines. Exa's 500-byte content cap is modeled. No search provider or public network is contacted.

Current harness mode: **production WebRelevanceTool**, fake source tool and local fake Jev HTTP server. No public network or real credentials.

Fake scores are intentionally easy to predict: task-relevant technical sections/results get score 4; irrelevant material gets score 0. Confidence is fixed at 0.99. This measures renderer/manifest size and policy behavior under a known decision pattern; it does **not** measure Jev relevance accuracy, real classifier latency, or billed token savings. All emitted drop-manifest text is included in AFTER token estimates.

Estimator: `ceil(utf8.RuneCountInString(strings.TrimSpace(text))/4)`, mirrored from the unexported `estimateTokensApprox` in `internal/http/instruction_blocks.go` and `scripts/jev_baseline.py`. This is an approximation, not a provider tokenizer. Input arguments, classifier requests/responses, and metadata are not counted as main-model tool-result text.
