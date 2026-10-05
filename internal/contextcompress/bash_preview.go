package contextcompress

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
)

const bashPreviewRunes = 16000 // ceil(runes/4) > 4000 tokens

// Only strip a leading ISO timestamp, not arbitrary numbers: diagnostic codes,
// locations and assertion values must never be normalized away.
var logTimestamp = regexp.MustCompile(`^\[?\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?\]?\s+`)

type bashRun struct {
	line      string
	count     int
	protected bool
}

func compressBashOutput(tr llm.ToolResult) string {
	lines := strings.Split(tr.Content, "\n")
	stderrStart := 0
	switch v := tr.Metadata["stderr_start_line"].(type) {
	case int:
		stderrStart = v
	case float64:
		stderrStart = int(v)
	}
	runs := make([]bashRun, 0, len(lines))
	for i, line := range lines {
		protected := isImportantLine(line) || strings.HasPrefix(line, "Exit code:") || (stderrStart > 0 && i+1 >= stderrStart)
		if len(runs) > 0 {
			last := &runs[len(runs)-1]
			// Distinct protected lines remain verbatim. Identical errors can be counted
			// without discarding any unique diagnostic text.
			identical := last.line == line
			near := !protected && !last.protected && logTimestamp.ReplaceAllString(last.line, "") == logTimestamp.ReplaceAllString(line, "")
			if identical || near {
				last.count++
				last.protected = last.protected || protected
				continue
			}
		}
		runs = append(runs, bashRun{line: line, count: 1, protected: protected})
	}
	var out strings.Builder
	omitted := 0
	flush := func() {
		if omitted > 0 {
			fmt.Fprintf(&out, "... [%d lines omitted] ...\n", omitted)
			omitted = 0
		}
	}
	for i, run := range runs {
		if i >= 20 && i < len(runs)-40 && !run.protected {
			omitted += run.count
			continue
		}
		flush()
		line := run.line
		if !run.protected && utf8.RuneCountInString(line) > 200 {
			r := []rune(line)
			line = string(r[:80]) + " ... [characters omitted] ... " + string(r[len(r)-80:])
		}
		out.WriteString(line)
		if run.count > 1 {
			fmt.Fprintf(&out, " [repeated %d times]", run.count)
		}
		out.WriteByte('\n')
	}
	flush()
	return strings.TrimRight(out.String(), "\n")
}

func largeBashResult(tr llm.ToolResult) bool {
	return utf8.RuneCountInString(strings.TrimSpace(tr.Content)) > bashPreviewRunes
}
