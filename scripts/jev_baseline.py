#!/usr/bin/env python3
"""Measure where context tokens go in past brute sessions.

Reads the session archive (JSONL event log written by internal/session/jsonl_writer.go,
and/or the `messages` table of aagent.db) and reports, per tool, how many tokens its
results added to the LLM context, plus heuristics for how much of that was never used.

Read-only: it never writes to the session store. Output is markdown, so it can be
pasted into docs/jev-baseline.md.

Usage:
  python3 brute/scripts/jev_baseline.py                       # default archive, 600 sessions
  python3 brute/scripts/jev_baseline.py --limit 0             # all sessions
  python3 brute/scripts/jev_baseline.py --db ~/.local/share/aagent/aagent.db --sessions-dir ''
"""

from __future__ import annotations

import argparse
import json
import math
import os
import random
import re
import sqlite3
import statistics
import sys
from collections import Counter, defaultdict

DEFAULT_SESSIONS_DIR = os.path.expanduser("~/.local/share/aagent/sessions")

# Mirrors estimateTokensApprox in internal/http/instruction_blocks.go:
# ceil(runeCount(trimmed) / 4). Python str length is already a rune count.
def tokens(text: str) -> int:
    if not text:
        return 0
    trimmed = text.strip()
    return math.ceil(len(trimmed) / 4.0) if trimmed else 0


# Tools that wrap other tools; their result payload is a JSON array of steps.
WRAPPER_TOOLS = {"parallel", "pipeline"}

# Identifier-ish tokens used for the "was this ever referenced later" heuristic.
IDENT_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_\-.]{3,}")

# Words too common to prove that the model actually reused a line of tool output.
STOPWORDS = frozenset(
    """
    this that then than with from into over under about above below after before where which while
    when what will would should could have hase been being does done make made need want used using
    true false null none nil void func type struct interface package import return const else elif
    case switch break continue default export default class async await function const let var new
    public private static final string int float bool error err ctx context name names value values
    data item items list index key keys line lines file files path paths text json http https
    self args kwargs print test tests spec specs todo note info warn debug trace null undefined
    """.split()
)


def distinctive(line: str) -> set[str]:
    out = set()
    for m in IDENT_RE.finditer(line):
        w = m.group(0).lower().strip(".-_")
        if len(w) >= 4 and w not in STOPWORDS:
            out.add(w)
    return out


def jsonish(value) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    return json.dumps(value, ensure_ascii=False)


def basename_keys(path: str) -> set[str]:
    """Keys that count as 'this file was mentioned again' for a read/edit target."""
    if not path:
        return set()
    base = os.path.basename(path.rstrip("/"))
    keys = {base.lower()}
    stem = os.path.splitext(base)[0]
    if len(stem) >= 4:
        keys.add(stem.lower())
    return {k for k in keys if len(k) >= 4}


class Stats:
    """Aggregates across all scanned sessions."""

    def __init__(self):
        self.sessions = 0
        self.messages = 0
        # context buckets (tokens actually shipped to the model)
        self.bucket_tokens = Counter()          # user / assistant_text / tool_call_args / tool_result
        self.bucket_weighted = Counter()        # same, multiplied by how many requests resend it
        self.image_payload_chars = 0
        # per tool
        self.calls = Counter()
        self.tool_tokens = Counter()
        self.tool_weighted = Counter()
        self.tool_samples = defaultdict(list)   # per-call token sizes
        self.tool_errors = Counter()
        self.tool_error_tokens = Counter()
        # waste heuristics
        self.unref_line_tokens = Counter()      # tokens on result lines never echoed later
        self.scored_line_tokens = Counter()     # tokens on lines we could score
        self.wholly_unref_calls = Counter()     # calls where no line was ever echoed
        self.read_calls_with_path = 0
        self.read_path_never_mentioned = 0
        self.read_path_never_mentioned_tokens = 0
        self.read_tokens_with_path = 0
        self.dup_read_calls = 0
        self.dup_read_tokens = 0
        # union of the two read-waste signals, counted once per call (they overlap)
        self.read_waste_union_calls = 0
        self.read_waste_union_tokens = 0
        self.wrapper_overhead_tokens = 0
        self.per_session_tool_tokens = []


def unwrap(name: str, content: str, args: dict) -> list[tuple[str, str, dict]]:
    """Expand parallel/pipeline results into (inner_tool, output, inner_args) triples.

    Inner args come from the wrapper call's own `steps` array, matched by the step
    index reported in the result, so a `read` nested in `parallel` still exposes its
    file path for the reuse heuristics.

    Returns [(name, content, args)] unchanged when the payload is not a step array.
    """
    if name not in WRAPPER_TOOLS or not content:
        return [(name, content, args)]
    body = content.lstrip()
    if body.startswith("Error:"):
        body = body[len("Error:"):]
    body = body.strip()
    if not body.startswith("["):
        return [(name, content, args)]
    try:
        steps = json.loads(body)
    except Exception:
        return [(name, content, args)]
    if not isinstance(steps, list):
        return [(name, content, args)]

    call_steps = args.get("steps") if isinstance(args, dict) else None
    call_steps = call_steps if isinstance(call_steps, list) else []

    out = []
    for pos, step in enumerate(steps):
        if not isinstance(step, dict):
            continue
        inner = step.get("tool") or name
        text = jsonish(step.get("output"))
        if step.get("error"):
            text = (text + "\n" + jsonish(step.get("error"))).strip()
        # result steps are 1-based; fall back to positional order
        idx = step.get("step")
        idx = (idx - 1) if isinstance(idx, int) and idx > 0 else pos
        inner_args = {}
        if 0 <= idx < len(call_steps) and isinstance(call_steps[idx], dict):
            cand = call_steps[idx].get("args")
            if isinstance(cand, dict):
                inner_args = cand
        out.append((inner, text, inner_args))
    return out or [(name, content, args)]


def extract_path(args) -> str:
    if not isinstance(args, dict):
        return ""
    for key in ("file_path", "path", "filepath", "file"):
        v = args.get(key)
        if isinstance(v, str) and v:
            return v
    return ""


def session_messages_from_jsonl(path: str):
    msgs = []
    try:
        with open(path, errors="replace") as fh:
            for line in fh:
                try:
                    evt = json.loads(line)
                except Exception:
                    continue
                if evt.get("event_type") != "message":
                    continue
                m = evt.get("message")
                if isinstance(m, dict):
                    msgs.append(m)
    except OSError:
        return []
    return msgs


def analyze_session(msgs: list[dict], st: Stats) -> None:
    """Single session pass: context accounting forward, reference check backward."""
    if not msgs:
        return

    call_names: dict[str, str] = {}
    call_args: dict[str, dict] = {}
    for m in msgs:
        for tc in m.get("tool_calls") or []:
            if isinstance(tc, dict):
                call_names[tc.get("id")] = tc.get("name") or "?"
                call_args[tc.get("id")] = tc.get("input") if isinstance(tc.get("input"), dict) else {}

    # How many assistant turns happen at or after index i -> how many requests resend
    # a message added at i. A payload injected early is paid for on every later turn.
    n = len(msgs)
    assistant_after = [0] * (n + 1)
    for i in range(n - 1, -1, -1):
        assistant_after[i] = assistant_after[i + 1] + (1 if msgs[i].get("role") == "assistant" else 0)

    # Reverse pass: tokens that the user or the model itself wrote *after* a tool result.
    # Tool results are deliberately excluded - one tool echoing another proves nothing
    # about the model having used the text.
    future_terms: set[str] = set()
    future_terms_at: list[set[str]] = [set()] * n
    for i in range(n - 1, -1, -1):
        future_terms_at[i] = set(future_terms)
        m = msgs[i]
        role = m.get("role")
        if role in ("assistant", "user"):
            future_terms |= distinctive(m.get("content") or "")
            for tc in m.get("tool_calls") or []:
                if isinstance(tc, dict):
                    future_terms |= distinctive(jsonish(tc.get("input")))

    reads_seen: Counter = Counter()
    session_tool_tokens = 0

    for i, m in enumerate(msgs):
        st.messages += 1
        role = m.get("role")
        weight = max(1, assistant_after[i])

        for img in m.get("images") or []:
            st.image_payload_chars += len(jsonish(img))

        content_tok = tokens(m.get("content") or "")
        if role == "user":
            st.bucket_tokens["user_text"] += content_tok
            st.bucket_weighted["user_text"] += content_tok * weight
        elif role == "assistant":
            st.bucket_tokens["assistant_text"] += content_tok
            st.bucket_weighted["assistant_text"] += content_tok * weight
            args_tok = sum(tokens(jsonish(tc)) for tc in (m.get("tool_calls") or []))
            st.bucket_tokens["tool_call_args"] += args_tok
            st.bucket_weighted["tool_call_args"] += args_tok * weight

        results = m.get("tool_results")
        if not isinstance(results, list):
            continue

        for entry in results:
            if not isinstance(entry, dict):
                continue
            raw = entry.get("content") or ""
            cid = entry.get("tool_call_id")
            name = entry.get("name") or call_names.get(cid) or "?"
            args = call_args.get(cid) or {}

            raw_tok = tokens(raw)
            st.bucket_tokens["tool_result"] += raw_tok
            st.bucket_weighted["tool_result"] += raw_tok * weight
            session_tool_tokens += raw_tok

            parts = unwrap(name, raw, args)
            if len(parts) > 1 or name in WRAPPER_TOOLS:
                st.wrapper_overhead_tokens += max(0, raw_tok - sum(tokens(p[1]) for p in parts))

            for inner, text, inner_args in parts:
                t = tokens(text)
                st.calls[inner] += 1
                st.tool_tokens[inner] += t
                st.tool_weighted[inner] += t * weight
                st.tool_samples[inner].append(t)

                low = text[:400].lower()
                if low.startswith("error") or "\nerror:" in low:
                    st.tool_errors[inner] += 1
                    st.tool_error_tokens[inner] += t

                # Line-level reuse heuristic.
                future = future_terms_at[i]
                scored = unref = 0
                any_ref = False
                for line in text.splitlines():
                    lt = tokens(line)
                    if lt == 0:
                        continue
                    terms = distinctive(line)
                    if not terms:
                        continue  # unscoreable (punctuation/numbers only)
                    scored += lt
                    if terms & future:
                        any_ref = True
                    else:
                        unref += lt
                st.scored_line_tokens[inner] += scored
                st.unref_line_tokens[inner] += unref
                if scored > 0 and not any_ref:
                    st.wholly_unref_calls[inner] += 1

                # File-level reuse + duplicate reads, for whole-file loads.
                if inner == "read":
                    path = extract_path(inner_args)
                    if path:
                        st.read_calls_with_path += 1
                        st.read_tokens_with_path += t
                        never_mentioned = not (basename_keys(path) & future)
                        if never_mentioned:
                            st.read_path_never_mentioned += 1
                            st.read_path_never_mentioned_tokens += t
                        reads_seen[path] += 1
                        is_dup = reads_seen[path] > 1
                        if is_dup:
                            st.dup_read_calls += 1
                            st.dup_read_tokens += t
                        if never_mentioned or is_dup:
                            st.read_waste_union_calls += 1
                            st.read_waste_union_tokens += t

    st.sessions += 1
    st.per_session_tool_tokens.append(session_tool_tokens)


def load_db_sessions(db_path: str):
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    rows = con.execute(
        "SELECT session_id, role, content, tool_calls, tool_results, timestamp "
        "FROM messages ORDER BY session_id, timestamp, rowid"
    ).fetchall()
    con.close()
    grouped = defaultdict(list)
    for r in rows:
        m = {"role": r["role"], "content": r["content"] or ""}
        for field in ("tool_calls", "tool_results"):
            if r[field]:
                try:
                    m[field] = json.loads(r[field])
                except Exception:
                    pass
        grouped[r["session_id"]].append(m)
    return list(grouped.values())


def pct(part: float, whole: float) -> str:
    return f"{(100.0 * part / whole):.1f}%" if whole else "-"


def selftest() -> int:
    """Checks the accounting rules on hand-built sessions with known answers."""
    fails = []

    def check(label, got, want):
        if got != want:
            fails.append(f"{label}: got {got!r}, want {want!r}")

    # --- session A: one read, one line reused, one line not; file never mentioned again
    body = "alpha_marker here\nunused_gamma_thing"
    a = [
        {"role": "user", "content": "please check it"},
        {"role": "assistant", "content": "ok",
         "tool_calls": [{"id": "c1", "name": "read", "input": {"file_path": "/x/zeta.go"}}]},
        {"role": "tool", "tool_results": [{"tool_call_id": "c1", "name": "read", "content": body}]},
        {"role": "assistant", "content": "found alpha_marker"},
    ]
    st = Stats()
    analyze_session(a, st)
    check("A read calls", st.calls["read"], 1)
    check("A read tokens", st.tool_tokens["read"], tokens(body))
    # one later assistant turn re-ships the result exactly once
    check("A read weighted", st.tool_weighted["read"], tokens(body) * 1)
    check("A unreferenced line", st.unref_line_tokens["read"], tokens("unused_gamma_thing"))
    check("A scored lines", st.scored_line_tokens["read"],
          tokens("alpha_marker here") + tokens("unused_gamma_thing"))
    # "alpha_marker" was echoed, so the call is not wholly unused
    check("A zero-reuse calls", st.wholly_unref_calls["read"], 0)
    # ...but zeta.go itself is never named again
    check("A path never mentioned", st.read_path_never_mentioned, 1)
    check("A dup reads", st.dup_read_calls, 0)
    check("A waste union calls", st.read_waste_union_calls, 1)
    check("A tool_result bucket", st.bucket_tokens["tool_result"], tokens(body))

    # --- session B: terms only in an EARLIER message must not count as reuse
    b = [
        {"role": "assistant", "content": "beta_token mentioned up front"},
        {"role": "assistant", "content": "go",
         "tool_calls": [{"id": "c1", "name": "grep", "input": {"pattern": "x"}}]},
        {"role": "tool", "tool_results": [{"tool_call_id": "c1", "name": "grep",
                                           "content": "beta_token"}]},
        {"role": "assistant", "content": "done"},
    ]
    st = Stats()
    analyze_session(b, st)
    check("B backward-looking match ignored", st.unref_line_tokens["grep"], tokens("beta_token"))

    # --- session C: duplicate read of the same path
    c = [
        {"role": "assistant", "content": "",
         "tool_calls": [{"id": "c1", "name": "read", "input": {"file_path": "/x/dup.go"}}]},
        {"role": "tool", "tool_results": [{"tool_call_id": "c1", "name": "read", "content": "dup_body_text"}]},
        {"role": "assistant", "content": "",
         "tool_calls": [{"id": "c2", "name": "read", "input": {"file_path": "/x/dup.go"}}]},
        {"role": "tool", "tool_results": [{"tool_call_id": "c2", "name": "read", "content": "dup_body_text"}]},
        {"role": "assistant", "content": "dup.go dup_body_text seen"},
    ]
    st = Stats()
    analyze_session(c, st)
    check("C read calls", st.calls["read"], 2)
    check("C dup reads", st.dup_read_calls, 1)
    check("C dup tokens", st.dup_read_tokens, tokens("dup_body_text"))
    check("C path mentioned again", st.read_path_never_mentioned, 0)
    check("C union counts dup once", st.read_waste_union_calls, 1)

    # --- session D: parallel unwrapping attributes inner tools and inner args
    steps_result = json.dumps([
        {"step": 1, "tool": "read", "success": True, "output": "inner_read_payload"},
        {"step": 2, "tool": "grep", "success": True, "output": "inner_grep_payload"},
    ])
    d = [
        {"role": "assistant", "content": "",
         "tool_calls": [{"id": "c1", "name": "parallel", "input": {"steps": [
             {"tool": "read", "args": {"file_path": "/x/inner.go"}},
             {"tool": "grep", "args": {"pattern": "q"}},
         ]}}]},
        {"role": "tool", "tool_results": [{"tool_call_id": "c1", "name": "parallel",
                                           "content": steps_result}]},
        {"role": "assistant", "content": "ok"},
    ]
    st = Stats()
    analyze_session(d, st)
    check("D inner read attributed", st.calls["read"], 1)
    check("D inner grep attributed", st.calls["grep"], 1)
    check("D parallel not counted as a tool", st.calls["parallel"], 0)
    check("D inner read tokens", st.tool_tokens["read"], tokens("inner_read_payload"))
    # inner args recovered from the wrapper call, so the path check still applies
    check("D inner path resolved", st.read_calls_with_path, 1)
    # raw payload counted once in the bucket; JSON framing shows up as overhead only
    check("D bucket is raw payload", st.bucket_tokens["tool_result"], tokens(steps_result))
    check("D wrapper overhead", st.wrapper_overhead_tokens,
          tokens(steps_result) - tokens("inner_read_payload") - tokens("inner_grep_payload"))

    # --- session E: resend weighting grows with the number of later assistant turns
    e = [
        {"role": "assistant", "content": "",
         "tool_calls": [{"id": "c1", "name": "bash", "input": {}}]},
        {"role": "tool", "tool_results": [{"tool_call_id": "c1", "name": "bash", "content": "out"}]},
        {"role": "assistant", "content": "a"},
        {"role": "assistant", "content": "b"},
        {"role": "assistant", "content": "c"},
    ]
    st = Stats()
    analyze_session(e, st)
    check("E bash weighted by 3 later turns", st.tool_weighted["bash"], tokens("out") * 3)

    # --- invariant on the real corpus shape: buckets never mix with per-tool counters
    st = Stats()
    for sess in (a, b, c, d, e):
        analyze_session(sess, st)
    check("sessions counted", st.sessions, 5)
    check("raw == unwrapped + wrapper overhead",
          st.bucket_tokens["tool_result"],
          sum(st.tool_tokens.values()) + st.wrapper_overhead_tokens)

    if fails:
        print("SELFTEST FAILED:")
        for f in fails:
            print("  -", f)
        return 1
    print("selftest: all checks passed")
    return 0


def report(st: Stats, args) -> str:
    total_ctx = sum(st.bucket_tokens.values())
    total_weighted = sum(st.bucket_weighted.values())
    tool_total = st.bucket_tokens["tool_result"]
    out = []
    w = out.append

    w(f"Sessions analysed: **{st.sessions}** (filter: >= {args.min_tool_results} tool results), "
      f"messages: **{st.messages:,}**, tool calls: **{sum(st.calls.values()):,}**")
    w(f"Total transcript context: **{total_ctx:,} tokens**; resend-weighted spend: "
      f"**{total_weighted:,} tokens**; image payloads excluded from token counts "
      f"({st.image_payload_chars/1e6:.1f} MB of base64).")
    w("")
    w("### Context composition")
    w("")
    w("| Bucket | Tokens | Share of transcript | Resend-weighted tokens | Share of weighted spend |")
    w("|---|---:|---:|---:|---:|")
    for k in ("tool_result", "assistant_text", "tool_call_args", "user_text"):
        w(f"| {k} | {st.bucket_tokens[k]:,} | {pct(st.bucket_tokens[k], total_ctx)} | "
          f"{st.bucket_weighted[k]:,} | {pct(st.bucket_weighted[k], total_weighted)} |")
    w("")
    w("### Per-tool result size")
    w("")
    w("| Tool | Calls | Total tokens | Avg | p50 | p95 | Max | % of tool output | % of transcript | % of weighted spend |")
    w("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
    for name, _ in st.tool_tokens.most_common(args.top):
        s = sorted(st.tool_samples[name])
        p50 = statistics.median(s) if s else 0
        p95 = s[min(len(s) - 1, int(0.95 * len(s)))] if s else 0
        avg = st.tool_tokens[name] / len(s) if s else 0
        w(f"| {name} | {st.calls[name]:,} | {st.tool_tokens[name]:,} | {avg:,.0f} | {p50:,.0f} | "
          f"{p95:,.0f} | {max(s) if s else 0:,} | {pct(st.tool_tokens[name], tool_total)} | "
          f"{pct(st.tool_tokens[name], total_ctx)} | {pct(st.tool_weighted[name], total_weighted)} |")
    w("")
    w("### Never-referenced estimate")
    w("")
    w("| Tool | Scoreable tokens | Never echoed later | Share | Calls w/ zero reuse | Error-result tokens |")
    w("|---|---:|---:|---:|---:|---:|")
    for name, _ in st.tool_tokens.most_common(args.top):
        sc = st.scored_line_tokens[name]
        w(f"| {name} | {sc:,} | {st.unref_line_tokens[name]:,} | {pct(st.unref_line_tokens[name], sc)} | "
          f"{st.wholly_unref_calls[name]:,} / {st.calls[name]:,} | {st.tool_error_tokens[name]:,} |")
    w("")
    w("### Whole-file read waste")
    w("")
    w(f"- `read` calls with a resolvable path: **{st.read_calls_with_path:,}** "
      f"({st.read_tokens_with_path:,} tokens)")
    w(f"- reads whose file is never mentioned again by the model: "
      f"**{st.read_path_never_mentioned:,}** calls, **{st.read_path_never_mentioned_tokens:,}** tokens "
      f"({pct(st.read_path_never_mentioned_tokens, st.read_tokens_with_path)} of read tokens)")
    w(f"- repeat reads of a path already read in the same session: "
      f"**{st.dup_read_calls:,}** calls, **{st.dup_read_tokens:,}** tokens "
      f"({pct(st.dup_read_tokens, st.read_tokens_with_path)} of read tokens)")
    w(f"- union of the two signals (counted once per call): **{st.read_waste_union_calls:,}** calls, "
      f"**{st.read_waste_union_tokens:,}** tokens "
      f"({pct(st.read_waste_union_tokens, st.read_tokens_with_path)} of read tokens)")
    w(f"- parallel/pipeline wrapper framing overhead: **{st.wrapper_overhead_tokens:,}** tokens")
    if st.per_session_tool_tokens:
        ss = sorted(st.per_session_tool_tokens)
        w(f"- tool-result tokens per session: median **{statistics.median(ss):,.0f}**, "
          f"p95 **{ss[min(len(ss)-1, int(0.95*len(ss)))]:,}**, max **{max(ss):,}**")
    return "\n".join(out)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sessions-dir", default=DEFAULT_SESSIONS_DIR)
    ap.add_argument("--db", default="", help="also fold in a aagent.db messages table")
    ap.add_argument("--min-tool-results", type=int, default=10,
                    help="skip sessions with fewer tool-result messages (noise/aborted runs)")
    ap.add_argument("--limit", type=int, default=600, help="sample size; 0 = all sessions")
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--top", type=int, default=18, help="tools to show in tables")
    ap.add_argument("--json", default="", help="also dump raw counters as JSON here")
    ap.add_argument("--selftest", action="store_true", help="verify the accounting rules and exit")
    args = ap.parse_args()

    if args.selftest:
        return selftest()

    st = Stats()
    skipped = 0

    if args.sessions_dir and os.path.isdir(args.sessions_dir):
        files = sorted(f for f in os.listdir(args.sessions_dir) if f.endswith(".jsonl"))
        random.seed(args.seed)
        if args.limit and args.limit < len(files):
            files = sorted(random.sample(files, args.limit))
        for i, f in enumerate(files, 1):
            msgs = session_messages_from_jsonl(os.path.join(args.sessions_dir, f))
            if sum(1 for m in msgs if m.get("role") == "tool") < args.min_tool_results:
                skipped += 1
                continue
            analyze_session(msgs, st)
            if i % 100 == 0:
                print(f"... {i}/{len(files)} files, {st.sessions} kept", file=sys.stderr)

    if args.db:
        for msgs in load_db_sessions(os.path.expanduser(args.db)):
            if sum(1 for m in msgs if m.get("role") == "tool") < args.min_tool_results:
                skipped += 1
                continue
            analyze_session(msgs, st)

    print(f"(skipped {skipped} sessions below the tool-result threshold)", file=sys.stderr)
    print(report(st, args))

    if args.json:
        with open(args.json, "w") as fh:
            json.dump({
                "sessions": st.sessions,
                "buckets": dict(st.bucket_tokens),
                "buckets_weighted": dict(st.bucket_weighted),
                "calls": dict(st.calls),
                "tool_tokens": dict(st.tool_tokens),
                "tool_weighted": dict(st.tool_weighted),
                "unref_line_tokens": dict(st.unref_line_tokens),
                "scored_line_tokens": dict(st.scored_line_tokens),
            }, fh, indent=2)
    return 0


if __name__ == "__main__":
    sys.exit(main())
