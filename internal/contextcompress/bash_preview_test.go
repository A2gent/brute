package contextcompress

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestBashPreviewThreshold(t *testing.T) {
	for _, n := range []int{16000, 16001} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			content := strings.Repeat("progress ready\n", 1200)
			content = content[:n-1] + "x"
			req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{Name: "bash", Content: content, IsError: true}}}}}
			_, result := NewCompressor(Config{Enabled: true}).CompressRequest(context.Background(), "s", req)
			if result.Applied != (n > 16000) {
				t.Fatalf("applied=%v at %d runes", result.Applied, n)
			}
		})
	}
}

func TestBashPreviewPreservesEveryErrorAndExitCode(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("ordinary progress line\n", 700))
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "ERROR unique diagnostic %d\n", i)
	}
	b.WriteString(strings.Repeat("ordinary progress line\n", 700))
	b.WriteString("Exit code: 7")
	original := b.String()
	c := NewCompressor(Config{Enabled: true})
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{Name: "bash", Content: original, IsError: true}}}}}
	out, result := c.CompressRequest(context.Background(), "s", req)
	if !result.Applied {
		t.Fatal("not compressed")
	}
	got := out.Messages[0].ToolResults[0].Content
	for i := 0; i < 200; i++ {
		want := fmt.Sprintf("ERROR unique diagnostic %d\n", i)
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q", want)
		}
	}
	if !strings.Contains(got, "Exit code: 7") {
		t.Fatal("lost exit code")
	}
	recovered, ok := c.Retrieve("s", result.Items[0].Hash, "")
	if !ok || recovered != original {
		t.Fatal("original not retrievable")
	}
}

func TestBashPreviewCollapse(t *testing.T) {
	content := strings.Repeat("ordinary ready\n", 1500) + "2026-10-05T10:00:01Z syncing packages\n2026-10-05T10:00:02Z syncing packages\nERROR code 401\nERROR code 403\nExit code: 1"
	c := NewCompressor(Config{Enabled: true})
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{Name: "bash", Content: content}}}}}
	out, result := c.CompressRequest(context.Background(), "s", req)
	if !result.Applied {
		t.Fatal("not compressed")
	}
	got := out.Messages[0].ToolResults[0].Content
	for _, want := range []string{"[repeated 1500 times]", "[repeated 2 times]", "ERROR code 401", "ERROR code 403", "Exit code: 1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestBashPreviewPreservesUnlabelledStderr(t *testing.T) {
	content := strings.Repeat("ordinary progress line\n", 900) + "compiler diagnostic at source.go:20\n" + strings.Repeat("ordinary trailing line\n", 300) + "Exit code: 1"
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{Name: "bash", Content: content, Metadata: map[string]interface{}{"stderr_start_line": float64(901)}}}}}}
	c := NewCompressor(Config{Enabled: true})
	out, result := c.CompressRequest(context.Background(), "s", req)
	if !result.Applied {
		t.Fatal("not compressed")
	}
	if !strings.Contains(out.Messages[0].ToolResults[0].Content, "compiler diagnostic at source.go:20") {
		t.Fatal("lost stderr")
	}
}

func TestBashPreviewNeverNormalizesDiagnosticNumbers(t *testing.T) {
	original := strings.Repeat("ordinary ready\n", 1500) + "source.go:12 value 401\nsource.go:13 value 403\nExit code: 0"
	got := compressBashOutput(llm.ToolResult{Name: "bash", Content: original})
	for _, want := range []string{"source.go:12 value 401", "source.go:13 value 403"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q", want)
		}
	}
}

func TestBashPreviewLongSingleLine(t *testing.T) {
	original := "HEAD" + strings.Repeat("x", 100000) + "TAIL"
	got := compressBashOutput(llm.ToolResult{Name: "bash", Content: original})
	if len(got) >= len(original) || !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") {
		t.Fatal("single-line head/tail missing")
	}
	// Protected lines intentionally override the preview budget.
	errorLine := "ERROR " + original
	if got := compressBashOutput(llm.ToolResult{Name: "bash", Content: errorLine}); got != errorLine {
		t.Fatal("truncated error line")
	}
}

func TestBashPreviewBoundsOrdinaryLongLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "line %d %s\n", i, strings.Repeat("x", 15000))
	}
	got := compressBashOutput(llm.ToolResult{Name: "bash", Content: b.String()})
	if len(got) > 16000 {
		t.Fatalf("ordinary preview too large: %d", len(got))
	}
	if !strings.Contains(got, "line 0") || !strings.Contains(got, "line 59") {
		t.Fatal("lost head/tail")
	}
}
