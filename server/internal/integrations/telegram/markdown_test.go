package telegram

import (
	"fmt"
	"strings"
	"testing"
)

// tagBalance reports the first structural defect in a Telegram HTML payload:
// a closing tag whose opener is not the innermost open one, or an element left
// open at the end. Telegram rejects both with "can't find end tag", and the
// sender then falls back to plain text — so a single unbalanced tag costs the
// user ALL formatting in that chunk, not just the offending span.
func tagBalance(s string) string {
	var stack []string
	for i := 0; i < len(s); {
		if s[i] != '<' {
			i++
			continue
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			return "unterminated tag"
		}
		tag := s[i+1 : i+j]
		if strings.HasPrefix(tag, "/") {
			name := tag[1:]
			if len(stack) == 0 || stack[len(stack)-1] != name {
				return "mismatched close " + name
			}
			stack = stack[:len(stack)-1]
		} else {
			if k := strings.IndexByte(tag, ' '); k >= 0 {
				tag = tag[:k]
			}
			stack = append(stack, tag)
		}
		i += j + 1
	}
	if len(stack) > 0 {
		return "unclosed " + strings.Join(stack, ",")
	}
	return ""
}

// Nesting is the defect that made whole chunks arrive as raw markdown:
// `***x***` used to render `<b><i>x</b></i>`, Telegram refused the payload, and
// the plain-text fallback showed the user every `**` in the message.
func TestFormatHTMLNestsEmphasisCorrectly(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"triple star", "***bold italic***", "<b><i>bold italic</i></b>"},
		{"bold around italic", "**a *b* c**", "<b>a <i>b</i> c</b>"},
		{"italic around bold", "*outer **inner** outer*", "<i>outer <b>inner</b> outer</i>"},
		{"strike around bold", "~~**b**~~", "<s><b>b</b></s>"},
		{"mixed triple and strike", "***a*** and ~~**b**~~", "<b><i>a</i></b> and <s><b>b</b></s>"},
		{"code span inside bold", "**bold with `code` inside**", "<b>bold with <code>code</code> inside</b>"},
		{"link inside bold", "**[label](https://x.co)**", `<b><a href="https://x.co">label</a></b>`},
	} {
		got := formatHTML(tc.in)
		if got != tc.want {
			t.Errorf("%s: formatHTML(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if defect := tagBalance(got); defect != "" {
			t.Errorf("%s: unbalanced output (%s): %q", tc.name, defect, got)
		}
	}
}

// Emphasis must not fire in ordinary prose. The previous regex pass italicised
// the middle of arithmetic and identifiers — `2 * 3 * 4 = 24` became
// `2 <i> 3 </i> 4 = 24` with the spaces inside the tag.
func TestFormatHTMLLeavesProseAlone(t *testing.T) {
	for _, in := range []string{
		"5 * 3 = 15 and 2 * 4 = 8",
		"ratio *ptr and *another",
		"NOTE: 2 * 2 * 2 = 8",
		"tree: src/**/test.go",
		"a lone * star and **two",
		"_underscore italic_ and __dunder bold__",
		"****",
	} {
		got := formatHTML(in)
		if strings.Contains(got, "<i>") || strings.Contains(got, "<b>") || strings.Contains(got, "<s>") {
			t.Errorf("formatHTML(%q) applied emphasis: %q", in, got)
		}
		if defect := tagBalance(got); defect != "" {
			t.Errorf("formatHTML(%q) unbalanced (%s): %q", in, defect, got)
		}
	}
}

// A URL may contain balanced parentheses; a bare regex stopped at the first
// `)`, truncating the href and leaving a stray `)` in the message.
func TestFormatHTMLLinkWithParenthesesInURL(t *testing.T) {
	got := formatHTML("[Go](https://en.wikipedia.org/wiki/Go_(language))")
	want := `<a href="https://en.wikipedia.org/wiki/Go_(language)">Go</a>`
	if got != want {
		t.Errorf("formatHTML = %q, want %q", got, want)
	}
}

// A destination Telegram will not open must degrade to text, not become a
// live link the reader has no reason to trust.
func TestFormatHTMLOnlyLinksRealURLSchemes(t *testing.T) {
	for _, in := range []string{
		"[x](javascript:alert(1))",
		"[x](vscode://file/etc/passwd)",
		"[x](data:text/html,<script>)",
	} {
		got := formatHTML(in)
		if strings.Contains(got, "<a href") {
			t.Errorf("formatHTML(%q) produced a link: %q", in, got)
		}
	}
	if got := formatHTML("[x](javascript:alert(1))"); got != "x (javascript:alert(1))" {
		t.Errorf("rejected link should degrade to text, got %q", got)
	}
}

// A backtick run opens a span of its own length, so a code span containing a
// single backtick survives instead of splitting at it.
func TestFormatHTMLCodeSpanWithInnerBacktick(t *testing.T) {
	got := formatHTML("``code with ` backtick``")
	want := "<code>code with ` backtick</code>"
	if got != want {
		t.Errorf("formatHTML = %q, want %q", got, want)
	}
}

// Every message the formatter can produce must be balanced, whatever the
// input. Fuzz-ish sweep over the marker combinations a receipt or a result
// block can realistically contain.
func TestFormatHTMLAlwaysBalanced(t *testing.T) {
	inputs := []string{
		"**unclosed bold", "~~unclosed strike", "`unclosed code", "*a **b***",
		"**a `b` c**", "***a *b* c***", "**a [l](https://x.co) b**",
		"# heading **with `code`**", "- bullet **bold** and `code`",
		"**SUMMARY**: `done`\n**RESULT**: ok",
		"a < b & c > d", "[a](", "``", "***", "**a*", "*a**",
		"**[a](https://x.co/a_(b))**", "mixed `code` and **bold** and ~~gone~~",
	}
	for _, in := range inputs {
		got := formatHTML(in)
		if defect := tagBalance(got); defect != "" {
			t.Errorf("formatHTML(%q) unbalanced (%s): %q", in, defect, got)
		}
	}
}

// A split inside a fenced code block must close the fence in one chunk and
// reopen it in the next. Each chunk is rendered independently, so a fence left
// open silently turns the continuation into prose — the user saw raw ``` and
// unconverted `**` for every message after the first.
func TestChunkMessageBalancesCodeFencesAcrossSplits(t *testing.T) {
	var b strings.Builder
	b.WriteString("Intro paragraph.\n\n")
	b.WriteString("```go\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "line%03d := compute(%d)\n", i, i)
	}
	b.WriteString("```\n")
	md := b.String()

	chunks := chunkMessage(md, maxMessageUnits)
	if len(chunks) < 2 {
		t.Fatalf("expected the reply to split, got %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks {
		if units := utf16Units(chunk); units > maxMessageUnits {
			t.Errorf("chunk %d is %d units, cap is %d", i, units, maxMessageUnits)
		}
		rendered := formatHTML(chunk)
		if open, closed := strings.Count(rendered, "<pre>"), strings.Count(rendered, "</pre>"); open != closed {
			t.Errorf("chunk %d rendered %d <pre> vs %d </pre>:\n%q", i, open, closed, chunk)
		}
		// The code must stay inside a code block in every piece: a `**` that
		// leaks out of the fence is exactly what the user complained about.
		if strings.Contains(rendered, "<b>") {
			t.Errorf("chunk %d leaked code content into emphasis:\n%s", i, rendered)
		}
	}
	// Continuation chunks reopen the fence so the language tag survives.
	for i := 1; i < len(chunks); i++ {
		if !strings.HasPrefix(chunks[i], "```go\n") {
			t.Errorf("chunk %d does not reopen the fence:\n%q", i, chunks[i])
		}
	}
}

// The fence repair must not disturb the existing split behavior: text without
// a code block still splits on the preferred newline.
func TestChunkMessageWithoutFenceUnchanged(t *testing.T) {
	text := strings.Repeat("a", 60) + "\n" + strings.Repeat("b", 60)
	chunks := chunkMessage(text, 100)
	if len(chunks) != 2 || !strings.HasPrefix(chunks[1], "b") {
		t.Fatalf("chunks = %#v", chunks)
	}
}

// An unterminated fence at the end of the input (a mid-stream snapshot, or an
// agent that never closed the block) is closed by the last chunk.
func TestChunkMessageClosesUnterminatedFence(t *testing.T) {
	md := "```go\n" + strings.Repeat("y\n", 100)
	for i, chunk := range chunkMessage(md, 120) {
		rendered := formatHTML(chunk)
		if strings.Count(rendered, "<pre>") != strings.Count(rendered, "</pre>") {
			t.Errorf("chunk %d left the fence open: %q", i, chunk)
		}
	}
}
