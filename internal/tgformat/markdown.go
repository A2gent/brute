// Package tgformat turns the Markdown that models write into the small HTML subset Telegram renders.
//
// Telegram does not understand Markdown headings, tables or nested lists; shown raw they are just noise on
// a phone. The conversion keeps what survives on a small screen (bold, italics, code, links, bullet lists,
// quotes) and drops the rest.
package tgformat

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	fenceRe     = regexp.MustCompile("^\\s*```\\s*([A-Za-z0-9_+#.-]*)\\s*$")
	headingRe   = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	bulletRe    = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	quoteRe     = regexp.MustCompile(`^\s*>\s?(.*)$`)
	hrRe        = regexp.MustCompile(`^\s*([-*_]\s*){3,}$`)
	tableRowRe  = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	tableSepRe  = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	inlineCode  = regexp.MustCompile("`([^`\n]+)`")
	linkRe      = regexp.MustCompile(`\[([^\]\n]+)\]\(((?:https?://|tg://|mailto:)[^\s)]+)\)`)
	boldStarRe  = regexp.MustCompile(`\*\*(.+?)\*\*`)
	boldUnderRe = regexp.MustCompile(`(^|[^\w_])__(.+?)__([^\w_]|$)`)
	strikeRe    = regexp.MustCompile(`~~(.+?)~~`)
	italicStar  = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*\n]*?)\*([^*\w]|$)`)
	italicUnder = regexp.MustCompile(`(^|[^\w_])_([^_\s][^_\n]*?)_([^\w_]|$)`)
	manyBlanks  = regexp.MustCompile(`\n{3,}`)
)

// MarkdownToHTML converts Markdown text into Telegram-safe HTML (parse_mode=HTML).
func MarkdownToHTML(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")

	var out []string
	var fence []string
	fenceLang := ""
	inFence := false
	var table []string
	var quote []string

	flushTable := func() {
		if len(table) == 0 {
			return
		}
		rows := make([]string, 0, len(table))
		for _, row := range table {
			if tableSepRe.MatchString(row) {
				continue
			}
			rows = append(rows, strings.TrimSpace(row))
		}
		out = append(out, "<pre>"+escape(strings.Join(rows, "\n"))+"</pre>")
		table = nil
	}
	flushQuote := func() {
		if len(quote) == 0 {
			return
		}
		formatted := make([]string, len(quote))
		for i, q := range quote {
			formatted[i] = inline(q)
		}
		out = append(out, "<blockquote>"+strings.Join(formatted, "\n")+"</blockquote>")
		quote = nil
	}
	flushFence := func() {
		code := escape(strings.Join(fence, "\n"))
		if fenceLang != "" {
			out = append(out, fmt.Sprintf(`<pre><code class="language-%s">%s</code></pre>`, escapeAttr(fenceLang), code))
		} else {
			out = append(out, "<pre>"+code+"</pre>")
		}
		fence, fenceLang = nil, ""
	}

	for _, line := range lines {
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			if inFence {
				flushFence()
				inFence = false
			} else {
				flushTable()
				flushQuote()
				inFence, fenceLang = true, m[1]
			}
			continue
		}
		if inFence {
			fence = append(fence, line)
			continue
		}

		if tableRowRe.MatchString(line) {
			flushQuote()
			table = append(table, line)
			continue
		}
		flushTable()

		if m := quoteRe.FindStringSubmatch(line); m != nil {
			quote = append(quote, m[1])
			continue
		}
		flushQuote()

		switch {
		case hrRe.MatchString(line) && !bulletRe.MatchString(line):
			out = append(out, "")
		case headingRe.MatchString(line):
			out = append(out, "<b>"+inline(headingRe.FindStringSubmatch(line)[1])+"</b>")
		case bulletRe.MatchString(line):
			m := bulletRe.FindStringSubmatch(line)
			indent := strings.Repeat("  ", len(strings.ReplaceAll(m[1], "\t", "  "))/2)
			out = append(out, indent+"• "+inline(m[2]))
		default:
			out = append(out, inline(line))
		}
	}
	if inFence {
		flushFence()
	}
	flushTable()
	flushQuote()

	return strings.TrimSpace(manyBlanks.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
}

// inline converts inline Markdown within one line (code, links, emphasis) and escapes everything else.
func inline(s string) string {
	var stash []string
	hold := func(html string) string {
		stash = append(stash, html)
		return fmt.Sprintf("\x00%d\x00", len(stash)-1)
	}

	s = inlineCode.ReplaceAllStringFunc(s, func(m string) string {
		return hold("<code>" + escape(inlineCode.FindStringSubmatch(m)[1]) + "</code>")
	})
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		return hold(`<a href="` + escapeAttr(sub[2]) + `">` + emphasis(escape(sub[1])) + "</a>")
	})
	s = emphasis(escape(s))

	for i := len(stash) - 1; i >= 0; i-- {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00%d\x00", i), stash[i])
	}
	return s
}

func emphasis(s string) string {
	s = boldStarRe.ReplaceAllString(s, "<b>$1</b>")
	s = boldUnderRe.ReplaceAllString(s, "$1<b>$2</b>$3")
	s = strikeRe.ReplaceAllString(s, "<s>$1</s>")
	s = italicStar.ReplaceAllString(s, "$1<i>$2</i>$3")
	s = italicUnder.ReplaceAllString(s, "$1<i>$2</i>$3")
	return s
}

func escape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

func escapeAttr(s string) string {
	return strings.ReplaceAll(escape(s), `"`, "&quot;")
}
