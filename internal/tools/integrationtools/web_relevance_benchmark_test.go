package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/llm/jev"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/tools"
)

// All corpus text is synthetic and all scores are fixed. These benchmarks measure
// admitted main-model result size, not real Jev relevance accuracy or billed tokens.
// The estimator mirrors internal/http.estimateTokensApprox (unexported) and
// scripts/jev_baseline.py; no provider-specific tokenizer exists in this package.
func webBenchTokens(text string) int {
	return (utf8.RuneCountInString(strings.TrimSpace(text)) + 3) / 4
}

type webBenchPage struct {
	file, title, url, task string
	keep                   []string
}

var webBenchPages = []webBenchPage{
	{"request_cancellation.md", "Go request cancellation", "https://example.test/go/request-cancellation",
		"Explain cancellation and deadlines for Go HTTP requests.", []string{"Request cancellation", "Cleanup and error handling"}},
	{"cache_revalidation.md", "Conditional HTTP caching", "https://example.test/http/cache-revalidation",
		"Explain HTTP cache validators, revalidation, and private response constraints.", []string{"Validators and revalidation", "Private and sensitive responses"}},
	{"sqlite_transactions.md", "SQLite transaction durability", "https://example.test/sqlite/transaction-durability",
		"Explain SQLite atomic commits, WAL, and power-loss durability.", []string{"Atomic transactions", "Journal modes and synchronization"}},
}

type webBenchCase struct {
	name, tool, output, task, retainedURL string
	params                                json.RawMessage
	keep                                  []string
}

// A copy per invocation avoids mutations of a shared Result masking regressions.
type webBenchSource struct{ name, output string }

func (s *webBenchSource) Name() string        { return s.name }
func (s *webBenchSource) Description() string { return "Synthetic offline benchmark source" }
func (s *webBenchSource) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (s *webBenchSource) Execute(_ context.Context, _ json.RawMessage) (*tools.Result, error) {
	return &tools.Result{Success: true, Output: s.output}, nil
}

func webBenchCorpus(tb testing.TB) []webBenchCase {
	tb.Helper()
	pages := make([]string, len(webBenchPages))
	for i, p := range webBenchPages {
		raw, err := os.ReadFile(filepath.Join("testdata", "web_relevance", p.file))
		if err != nil {
			tb.Fatal(err)
		}
		pages[i] = string(raw)
		if !utf8.ValidString(pages[i]) || !strings.Contains(pages[i], "Synthetic local sample") {
			tb.Fatalf("fixture %s must be valid UTF-8 and labeled synthetic", p.file)
		}
	}
	var cases []webBenchCase
	for target, p := range webBenchPages {
		params, _ := json.Marshal(map[string]interface{}{"url": p.url, "task_context": p.task})
		cases = append(cases, webBenchCase{"fetch/" + strings.TrimSuffix(p.file, ".md"), "fetch_url", pages[target], p.task, p.url, params, p.keep})
		for _, tool := range []string{"tavily_search", "exa_search", "brave_search_query"} {
			var out strings.Builder
			provider := map[string]string{"tavily_search": "Tavily", "exa_search": "Exa", "brave_search_query": "Brave"}[tool]
			fmt.Fprintf(&out, "%s Search results for %q\n", provider, p.task)
			for i, candidate := range webBenchPages {
				fmt.Fprintf(&out, "\n%d. %s\nURL: %s\n", i+1, candidate.title, candidate.url)
				content := webBenchExcerpt(pages[i], candidate.keep)
				label := "Snippet"
				if tool == "exa_search" {
					label = "Content"
					if len(content) > 500 {
						content = content[:500] + "..."
					}
				}
				fmt.Fprintf(&out, "%s: %s\n", label, content)
				if tool == "tavily_search" {
					fmt.Fprintln(&out, "Score: 0.900")
				}
				if tool == "brave_search_query" {
					fmt.Fprintln(&out, "Age: 1 day ago")
				}
			}
			// No task_context: searches must fall back to the query.
			params, _ := json.Marshal(map[string]interface{}{"query": p.task})
			cases = append(cases, webBenchCase{tool + "/" + strings.TrimSuffix(p.file, ".md"), tool, out.String(), p.task, p.url, params, []string{p.title}})
		}
	}
	return cases
}

func webBenchExcerpt(page string, titles []string) string {
	var parts []string
	for _, section := range strings.Split(page, "\n## ")[1:] {
		title, body, _ := strings.Cut(section, "\n")
		for _, keep := range titles {
			if title == keep {
				parts = append(parts, strings.TrimSpace(body))
			}
		}
	}
	return strings.Join(parts, " ")
}

func TestWebRelevanceTokenEstimator(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{{"", 0}, {" \n\t", 0}, {"abcd", 1}, {"abcde", 2}, {"  界🙂éa  ", 1}, {"界🙂éab", 2}} {
		if got := webBenchTokens(tc.text); got != tc.want {
			t.Errorf("tokens(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestWebRelevanceBenchmarkCorpus(t *testing.T) {
	cases := webBenchCorpus(t)
	if len(cases) != 12 {
		t.Fatalf("want 3 fetch and 9 search cases, got %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.output) >= 64*1024 || webBenchTokens(tc.output) == 0 {
				t.Fatalf("fixture must be nonempty and below the default classification cap")
			}
			var params map[string]interface{}
			if err := json.Unmarshal(tc.params, &params); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(tc.output, tc.retainedURL) {
				t.Fatal("original fixture must contain its relevant citation URL")
			}
			if tc.tool != "fetch_url" {
				if _, present := params["task_context"]; present {
					t.Fatal("search fixtures must exercise query-context fallback")
				}
				if strings.Count(tc.output, "\nURL: ") != 3 {
					t.Fatal("search fixtures must have three titled URL items")
				}
			}
			for _, keep := range tc.keep {
				if !strings.Contains(tc.output, keep) {
					t.Fatalf("fixture lacks expected relevant title %q", keep)
				}
			}
		})
	}
}

// Exercise the production wrapper through the same System One HTTP boundary.
func webBenchFilter(tb testing.TB, tc webBenchCase) string {
	tb.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			tb.Error(err)
		}
		answers := map[string]any{}
		state := req.StateString()
		for key := range req.Questions {
			_, rest, _ := strings.Cut(state, key+":\n")
			candidate, _, _ := strings.Cut(rest, "\nitem_")
			score := 0
			for _, keep := range tc.keep {
				if strings.Contains(candidate, keep) {
					score = 4
				}
			}
			answers[key] = map[string]any{"score": score, "confidence": .99}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	defer srv.Close()
	wrapper := NewWebRelevanceTool(&webBenchSource{name: tc.tool, output: tc.output}, jev.NewClient("fake-key", "", srv.URL), WebRelevanceOptions{Enabled: true})
	result, err := wrapper.Execute(context.Background(), tc.params)
	if err != nil || result == nil || !result.Success {
		tb.Fatalf("wrapper failed: %+v %v", result, err)
	}
	return result.Output
}

func TestWebRelevanceTokenBenchmark(t *testing.T) {
	t.Log("MODE=production-wrapper; synthetic fixtures; fake Jev HTTP server; no credentials/public network")
	t.Log("case\tbefore_tokens\tafter_tokens\tsaved_tokens\tsaved_percent")
	beforeTotal, afterTotal := 0, 0
	for _, tc := range webBenchCorpus(t) {
		source := &webBenchSource{name: tc.tool, output: tc.output}
		original, err := source.Execute(context.Background(), tc.params)
		if err != nil || original == nil || !original.Success || original.Output != tc.output {
			t.Fatalf("source tool failed: result=%+v err=%v", original, err)
		}
		after := webBenchFilter(t, tc)
		for _, keep := range tc.keep {
			if !strings.Contains(after, keep) {
				t.Fatalf("%s: lost retained title %q", tc.name, keep)
			}
		}
		if !strings.Contains(after, tc.retainedURL) || !strings.Contains(after, "Dropped by relevance filter") || !strings.Contains(after, "filter_results=false") {
			t.Fatalf("%s: missing citation, manifest, or recovery", tc.name)
		}
		if strings.Count(after, "score 0.00") < 2 {
			t.Fatalf("%s: dropped items must be disclosed", tc.name)
		}
		before, filtered := webBenchTokens(tc.output), webBenchTokens(after)
		beforeTotal += before
		afterTotal += filtered
		t.Logf("%s\t%d\t%d\t%d\t%.1f%%", tc.name, before, filtered, before-filtered, 100*float64(before-filtered)/float64(before))
	}
	t.Logf("TOTAL\t%d\t%d\t%d\t%.1f%%", beforeTotal, afterTotal, beforeTotal-afterTotal, 100*float64(beforeTotal-afterTotal)/float64(beforeTotal))
}

var webBenchOutputSink string

func BenchmarkWebRelevanceTokens(b *testing.B) {
	for _, tc := range webBenchCorpus(b) {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				webBenchOutputSink = webBenchFilter(b, tc)
			}
			b.StopTimer()
			before, after := webBenchTokens(tc.output), webBenchTokens(webBenchOutputSink)
			b.ReportMetric(float64(before), "before-tokens/op")
			b.ReportMetric(float64(after), "after-tokens/op")
			b.ReportMetric(float64(before-after), "saved-tokens/op")
		})
	}
}
