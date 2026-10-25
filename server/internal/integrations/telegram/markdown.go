package telegram

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// This file converts the agent's standard Markdown to Telegram HTML parse
// mode. HTML is chosen over MarkdownV2 deliberately: MarkdownV2 requires
// escaping 18 punctuation characters in ALL text (one missed escape fails the
// whole sendMessage), while HTML only needs &, <, > escaped and unknown tags
// are the only failure mode. The conversion is line-oriented and intentionally
// conservative — unrecognized markdown passes through as escaped plain text
// rather than risking a rejected message.
//
// The emphasis/link/code pass is a small recursive parser rather than a chain
// of regex replacements. Ordered regex passes cannot express nesting: running
// bold over `***x***` produced `<b><i>x</b></i>`, which Telegram rejects as an
// unmatched end tag, and the sender's plain-text fallback then showed the
// user raw `**` and backticks for the WHOLE chunk. Building the tags by
// recursion means the output is well-formed by construction.

var (
	reHeading = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	reBullet  = regexp.MustCompile(`^(\s*)[-*]\s+`)
)

// formatHTML renders markdown text as Telegram HTML. Code blocks are handled
// first (their content must stay verbatim, only entity-escaped); the rest is
// converted line by line.
func formatHTML(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	inCode := false
	var codeLang string
	var codeBuf []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				out.WriteString(renderCodeBlock(codeLang, codeBuf))
				out.WriteString("\n")
				inCode, codeBuf, codeLang = false, nil, ""
			} else {
				inCode = true
				codeLang = strings.TrimPrefix(trimmed, "```")
			}
			continue
		}
		if inCode {
			codeBuf = append(codeBuf, line)
			continue
		}
		out.WriteString(formatLine(line))
		out.WriteString("\n")
	}
	if inCode {
		// Unterminated fence (mid-stream snapshot, or a chunk boundary the
		// chunker could not balance): render what we have so the throttled
		// edit still shows the partial block.
		out.WriteString(renderCodeBlock(codeLang, codeBuf))
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func renderCodeBlock(lang string, lines []string) string {
	body := html.EscapeString(strings.Join(lines, "\n"))
	if lang != "" {
		return `<pre><code class="language-` + html.EscapeString(lang) + `">` + body + `</code></pre>`
	}
	return "<pre>" + body + "</pre>"
}

// formatLine converts one non-code line.
func formatLine(line string) string {
	if m := reHeading.FindStringSubmatch(line); m != nil {
		return "<b>" + formatInline(m[1]) + "</b>"
	}
	if m := reBullet.FindStringSubmatchIndex(line); m != nil {
		return line[m[2]:m[3]] + "• " + formatInline(line[m[1]:])
	}
	return formatInline(line)
}

// formatInline renders one line's inline markdown.
//
// Code spans and links are lifted out into placeholders BEFORE the emphasis
// pass and substituted back afterwards. Extracting them first keeps their
// content verbatim, and leaving a placeholder in the text is what lets
// emphasis SPAN them: rendering each fragment independently turned
// `**bold with `code` inside**` into literal `**` around a working code span,
// because the closing `**` had already been cut off with the following
// fragment.
func formatInline(s string) string {
	var spans []string
	s = extractInlineSpans(s, &spans)
	s = renderEmphasis(s)
	for i, rendered := range spans {
		s = strings.Replace(s, inlinePlaceholder(i), rendered, 1)
	}
	return s
}

// inlinePlaceholder marks where an extracted span re-inserts. NUL and 0x01
// cannot appear in a Telegram message body and carry no meaning to the
// emphasis parser, so a placeholder survives escaping untouched.
func inlinePlaceholder(i int) string {
	return "\x00" + strconv.Itoa(i) + "\x01"
}

// extractInlineSpans replaces every code span and link in s with its
// placeholder, appending the rendered replacement in placeholder order.
func extractInlineSpans(s string, spans *[]string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '`':
			if content, next, ok := scanCodeSpan(s, i); ok {
				out.WriteString(inlinePlaceholder(len(*spans)))
				*spans = append(*spans, "<code>"+html.EscapeString(content)+"</code>")
				i = next
				continue
			}
			out.WriteByte('`')
			i++
		case '[':
			if label, url, next, ok := scanLink(s, i); ok {
				out.WriteString(inlinePlaceholder(len(*spans)))
				*spans = append(*spans, renderLink(label, url))
				i = next
				continue
			}
			out.WriteByte('[')
			i++
		default:
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

// renderLink renders `[label](url)`. Only schemes Telegram will actually open
// are turned into an anchor: a `javascript:` or `vscode:` target rendered as a
// link either is refused (failing the whole chunk) or is a live link the user
// has no reason to trust, since the text came from the agent. Anything else
// degrades to `label (url)` as escaped text. The label is rendered as its own
// inline pass so emphasis and code spans inside it still apply.
func renderLink(label, url string) string {
	if !isLinkableURL(url) {
		return formatInline(label) + " (" + html.EscapeString(url) + ")"
	}
	return `<a href="` + html.EscapeString(url) + `">` + formatInline(label) + `</a>`
}

func isLinkableURL(url string) bool {
	if url == "" {
		return false
	}
	for _, r := range url {
		if r < 0x20 || r == 0x7f || r == ' ' || r == '"' || r == '\'' || r == '<' || r == '>' || r == '\\' {
			return false
		}
	}
	lower := strings.ToLower(url)
	for _, scheme := range []string{"http://", "https://", "mailto:", "tg://"} {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	return false
}

// scanCodeSpan matches a backtick run at s[start] against the next run of the
// same length, so a code span containing a single backtick survives
// instead of being split at it.
func scanCodeSpan(s string, start int) (content string, next int, ok bool) {
	n := 0
	for start+n < len(s) && s[start+n] == '`' {
		n++
	}
	for i := start + n; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(s) && s[i+run] == '`' {
			run++
		}
		if run == n {
			return s[start+n : i], i + run, true
		}
		i += run
	}
	return "", 0, false
}

// scanLink matches `[label](url)`. The label may contain nested brackets and
// the URL may contain balanced parentheses — the latter is what
// `[Go](https://en.wikipedia.org/wiki/Go_(language))` needs, and the previous
// regex form truncated it to `...Go_(language` plus a stray `)`.
func scanLink(s string, start int) (label, url string, next int, ok bool) {
	depth := 0
	close := -1
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				close = i
			}
		}
		if close >= 0 {
			break
		}
	}
	if close < 0 || close+1 >= len(s) || s[close+1] != '(' {
		return "", "", 0, false
	}
	depth = 1
	end := -1
	for i := close + 2; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		case ' ', '\n', '\t':
			// A destination with whitespace is not a link; leave the text as-is
			// rather than guessing where it ends.
			return "", "", 0, false
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return "", "", 0, false
	}
	return s[start+1 : close], s[close+2 : end], end + 1, true
}

// emphasisDelimiter is one supported inline emphasis marker.
type emphasisDelimiter struct {
	marker string
	open   string
	close  string
}

// Ordered longest-first: `***` must win over `**`, and `**` over `*`.
var emphasisDelimiters = []emphasisDelimiter{
	{"***", "<b><i>", "</i></b>"},
	{"**", "<b>", "</b>"},
	{"~~", "<s>", "</s>"},
	{"*", "<i>", "</i>"},
}

// renderEmphasis escapes s and converts the supported emphasis markers,
// recursing into each matched span so nested markers produce correctly nested
// tags and can never cross.
func renderEmphasis(s string) string {
	var out strings.Builder
	for s != "" {
		if d, content, rest, ok := matchEmphasis(s); ok {
			out.WriteString(d.open)
			out.WriteString(renderEmphasis(content))
			out.WriteString(d.close)
			s = rest
			continue
		}
		// No marker at this position: emit text up to the next possible one.
		next := len(s)
		if i := strings.IndexAny(s[1:], "*~"); i >= 0 {
			next = i + 1
		}
		out.WriteString(html.EscapeString(s[:next]))
		s = s[next:]
	}
	return out.String()
}

// matchEmphasis matches a delimiter at the start of s, returning the rendered
// tags, the raw inner text and the remaining input.
//
// Both ends are required to be "flanking": an opener may not be followed by
// whitespace and a closer may not be preceded by whitespace. That rule is what
// keeps ordinary prose intact — without it `2 * 3 * 4 = 24` became
// `2 <i> 3 </i> 4 = 24` and `*ptr and *another` italicised the middle of a
// sentence.
func matchEmphasis(s string) (d emphasisDelimiter, content, rest string, ok bool) {
	for _, d := range emphasisDelimiters {
		if !strings.HasPrefix(s, d.marker) {
			continue
		}
		inner := len(d.marker)
		if inner >= len(s) || isSpaceByte(s[inner]) {
			continue
		}
		// An opener must not be part of a longer run of the same marker; that
		// run belongs to the longer delimiter.
		if inner < len(s) && s[inner] == d.marker[0] {
			continue
		}
		if end, found := findCloser(s, inner, d.marker); found {
			if strings.TrimSpace(s[inner:end]) == "" {
				continue
			}
			return d, s[inner:end], s[end+len(d.marker):], true
		}
	}
	return emphasisDelimiter{}, "", "", false
}

// findCloser locates the next run of exactly marker that closes an emphasis
// span opened at from. Runs of a different length are skipped whole, so the
// `**` in `*outer **inner** outer*` cannot terminate the outer italics.
func findCloser(s string, from int, marker string) (int, bool) {
	ch := marker[0]
	for i := from; i+len(marker) <= len(s); i++ {
		if s[i] != ch {
			continue
		}
		// Skip past the whole run so a shorter delimiter never lands inside a
		// longer one.
		run := 0
		for i+run < len(s) && s[i+run] == ch {
			run++
		}
		if run == len(marker) && i > from && !isSpaceByte(s[i-1]) {
			return i, true
		}
		i += run - 1
	}
	return 0, false
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
