package http

// Text conversion between Slack mrkdwn and the agent: inbound decoding, outbound escaping,
// Markdown conversion and message chunking.

import (
	"regexp"
	"strings"
)

const slackMaxChunkRunes = 3500

var (
	slackEntityPattern   = regexp.MustCompile(`<([^<>\n]+)>`)
	slackCodePattern     = regexp.MustCompile("(?s)```.*?(?:```|$)|`[^`\n]+`")
	slackHeadingPattern  = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.+?)[ \t]*$`)
	slackBoldPattern     = regexp.MustCompile(`\*\*([^\n]+?)\*\*`)
	slackLinkPattern     = regexp.MustCompile(`!?\[([^\]\n]+)\]\((https?://[^\s)|]+)\)`)
	slackInboundUnescape = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")
	slackOutboundEscape  = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
)

// slackDecodeText turns Slack's wire format into plain text, resolving user mentions with nameOf.
func slackDecodeText(text string, nameOf func(userID string) string) string {
	decoded := slackEntityPattern.ReplaceAllStringFunc(text, func(match string) string {
		value, label, hasLabel := strings.Cut(match[1:len(match)-1], "|")
		switch {
		case strings.HasPrefix(value, "@"):
			return "@" + nameOf(value[1:])
		case strings.HasPrefix(value, "#"):
			if hasLabel && label != "" {
				return "#" + label
			}
			return value
		case strings.HasPrefix(value, "!"):
			if hasLabel && label != "" {
				return label
			}
			return "@" + strings.TrimPrefix(value, "!")
		case hasLabel && label != "" && label != value:
			return label + " (" + value + ")"
		default:
			return value
		}
	})
	return slackInboundUnescape.Replace(decoded)
}

// slackFormatReply makes agent output safe for Slack. Escaping comes first: it is what stops the
// output from producing <!channel> pings or <@U...> mentions, so the conversions below only ever
// add markup they built themselves.
func slackFormatReply(text string) string {
	escaped := slackOutboundEscape.Replace(text)
	var out strings.Builder
	last := 0
	for _, span := range slackCodePattern.FindAllStringIndex(escaped, -1) {
		out.WriteString(slackMarkdownToMrkdwn(escaped[last:span[0]]))
		out.WriteString(escaped[span[0]:span[1]])
		last = span[1]
	}
	out.WriteString(slackMarkdownToMrkdwn(escaped[last:]))
	return out.String()
}

func slackMarkdownToMrkdwn(text string) string {
	text = slackHeadingPattern.ReplaceAllStringFunc(text, func(line string) string {
		title := slackHeadingPattern.FindStringSubmatch(line)[1]
		return "**" + strings.ReplaceAll(title, "**", "") + "**"
	})
	text = slackBoldPattern.ReplaceAllString(text, "*$1*")
	// The address stays visible: a <url|label> link from injected output could hide where it leads.
	return slackLinkPattern.ReplaceAllString(text, "$1 (<$2>)")
}

// slackChunks splits formatted text into messages of at most limit runes. It prefers line breaks,
// never cuts through an entity or a link, and keeps code fences balanced in every chunk.
func slackChunks(text string, limit int) []string {
	const fence = "```"
	var chunks []string
	remaining := []rune(strings.TrimSpace(text))
	reopen := false
	for len(remaining) > 0 {
		prefix := ""
		if reopen {
			prefix = fence + "\n"
		}
		budget := limit - len(prefix) - len("\n"+fence)
		cut := len(remaining)
		if len(prefix)+cut > limit {
			cut = slackCutPoint(remaining, budget)
		}
		chunk := prefix + strings.TrimRight(string(remaining[:cut]), " \n")
		reopen = strings.Count(chunk, fence)%2 == 1
		if reopen {
			chunk += "\n" + fence
		}
		chunks = append(chunks, chunk)
		remaining = []rune(strings.TrimLeft(string(remaining[cut:]), " \n"))
	}
	return chunks
}

func slackCutPoint(text []rune, budget int) int {
	cut := budget
	for _, separator := range []rune{'\n', ' '} {
		if at := slackLastRune(text[:budget], separator); at > budget/2 {
			cut = at + 1
			break
		}
	}
	if open := slackLastRune(text[:cut], '<'); open > slackLastRune(text[:cut], '>') && open > 0 {
		cut = open
	}
	if amp := slackLastRune(text[:cut], '&'); amp > 0 && amp >= cut-len("&amp;") && amp > slackLastRune(text[:cut], ';') {
		cut = amp
	}
	return cut
}

func slackLastRune(text []rune, target rune) int {
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == target {
			return i
		}
	}
	return -1
}
