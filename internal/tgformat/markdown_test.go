package tgformat

import "testing"

func TestMarkdownToHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bold and italic", "**Hi** and *there* and ~~gone~~", "<b>Hi</b> and <i>there</i> and <s>gone</s>"},
		{"heading with emoji becomes a bold line", "## 🍿 В кино (премьеры 9 октября)", "<b>🍿 В кино (премьеры 9 октября)</b>"},
		{"dash and star bullets", "- one\n* two\n  - nested", "• one\n• two\n  • nested"},
		{"bold inside a bullet", "- **Other Mommy** - хоррор", "• <b>Other Mommy</b> - хоррор"},
		{"inline code is escaped and protected", "use `a<b> **x**` now", "use <code>a&lt;b&gt; **x**</code> now"},
		{"html in text is escaped", "1 < 2 & 3 > 2", "1 &lt; 2 &amp; 3 &gt; 2"},
		{"snake_case is not italic", "call my_function_name here", "call my_function_name here"},
		{"underscore italics", "an _emphasised_ word", "an <i>emphasised</i> word"},
		{"math stars are not italic", "5 * 3 * 2", "5 * 3 * 2"},
		{"link", "see [docs](https://example.com/a?b=1&c=2)", `see <a href="https://example.com/a?b=1&amp;c=2">docs</a>`},
		{"unsafe link scheme stays text", "[x](javascript:alert(1))", "[x](javascript:alert(1))"},
		{"fenced code with language", "```go\nfmt.Println(\"<hi>\")\n```", "<pre><code class=\"language-go\">fmt.Println(\"&lt;hi&gt;\")</code></pre>"},
		{"unclosed fence is closed", "```\ncode", "<pre>code</pre>"},
		{"table becomes preformatted without separator row", "| a | b |\n|---|---|\n| 1 | 2 |", "<pre>| a | b |\n| 1 | 2 |</pre>"},
		{"quote", "> wise words\n> more", "<blockquote>wise words\nmore</blockquote>"},
		{"horizontal rule is dropped", "one\n\n---\n\ntwo", "one\n\ntwo"},
		{"extra blank lines collapse", "a\n\n\n\n\nb", "a\n\nb"},
		{"numbered list is kept", "1. first\n2. second", "1. first\n2. second"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarkdownToHTML(tc.in); got != tc.want {
				t.Fatalf("\ninput: %q\n got:  %q\n want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarkdownToHTMLRealReply(t *testing.T) {
	in := "Вот главное.\n\n## 🍿 В кино\n- **Other Mommy** - хоррор Роба Сэвиджа.\n\n**16 октября выходят:** Street Fighter"
	want := "Вот главное.\n\n<b>🍿 В кино</b>\n• <b>Other Mommy</b> - хоррор Роба Сэвиджа.\n\n<b>16 октября выходят:</b> Street Fighter"
	if got := MarkdownToHTML(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
