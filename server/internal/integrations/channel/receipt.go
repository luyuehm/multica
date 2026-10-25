package channel

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// Agent text that reaches a chat channel is whatever the agent finished with.
// Some agents sign off by emitting the task's machine-readable receipt — a
// single JSON object with task_id/status/summary/result/verify/next_step — and
// every adapter forwarded it verbatim. The user then read a wall of raw JSON in
// Telegram: no rendering, escaped quotes, and internal bookkeeping fields
// (context_rule, prompt, stack traces) they were never meant to see.
//
// NormalizeAgentText is the one place that turns that shape into a readable
// message. It is deliberately narrow:
//
//   - It only rewrites text that is ENTIRELY one JSON object carrying at least
//     one receipt field. Ordinary prose, or a JSON snippet quoted inside a
//     sentence, passes through byte-for-byte — an agent asked to return JSON
//     gets to return JSON.
//   - It never drops a field the user asked for. Unknown keys survive under a
//     details section; only credentials and process bookkeeping are removed.
//
// The result is Markdown, which every adapter already renders (Telegram
// converts it to HTML, Lark wraps it in a markdown card, WeCom and DingTalk
// render it natively).

// receiptFields are the contract fields, in the order a reader wants them.
var receiptFields = []string{"task_id", "identifier", "status", "agent", "summary", "result", "verify", "files", "next_step"}

// receiptSignalFields is the subset whose presence proves the object is a
// receipt rather than an arbitrary JSON value the user asked the agent for.
// task_id alone is not enough — an agent might legitimately dump one task row.
var receiptSignalFields = []string{"status", "summary", "result", "verify", "next_step", "task_id", "files"}

// droppedReceiptFields are never shown: credentials the platform redacts
// anyway, and process bookkeeping (the contract's own context_rule tells the
// reader how to treat the message; it is not part of the message).
var droppedReceiptFields = map[string]bool{
	"context_rule": true,
	"prompt":       true,
	"source":       true,
	"context":      true,
	"stack":        true,
	"stacktrace":   true,
	"traceback":    true,
	"debug":        true,
	"trace":        true,
	"meta":         true,
	"metadata":     true,
	"raw":          true,
	"raw_output":   true,
	"log":          true,
	"logs":         true,
	"_":            true,
}

// sensitiveFieldHints marks keys whose value is a secret regardless of what it
// looks like — redact.Text only catches recognizable token shapes.
var sensitiveFieldHints = []string{
	"token", "secret", "password", "passwd", "credential", "api_key", "apikey",
	"authorization", "auth", "cookie", "private_key", "session", "dsn",
}

const (
	// receiptValueMaxRunes bounds one rendered field so a runaway result blob
	// cannot push the rest of the receipt out of the first message.
	receiptValueMaxRunes = 1200
	// receiptTotalMaxRunes bounds the whole rendering before the channel's own
	// chunking takes over. A receipt longer than this is no longer a summary.
	receiptTotalMaxRunes = 3600
)

// NormalizeAgentText renders a machine-readable task receipt as readable
// Markdown. Text that is not exactly one receipt object is returned unchanged.
func NormalizeAgentText(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return text
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return text
	}
	if !looksLikeReceipt(obj) {
		return text
	}
	return renderReceipt(obj)
}

// looksLikeReceipt reports whether obj is a task receipt rather than a JSON
// document the user asked for.
//
// The gate is two conditions, and both are needed. A receipt must carry an
// outcome word (status or summary), and it must carry at least TWO contract
// fields. Requiring an outcome word alone would rewrite an API response the
// user explicitly asked the agent to return — `{"status":"ok","results":[…]}` —
// into a "receipt" and hide the payload inside a details list.
func looksLikeReceipt(obj map[string]any) bool {
	_, hasStatus := obj["status"]
	_, hasSummary := obj["summary"]
	if !hasStatus && !hasSummary {
		return false
	}
	present := 0
	for _, key := range receiptSignalFields {
		if value, ok := obj[key]; ok && isMeaningful(value) {
			present++
		}
	}
	return present >= 2
}

func isMeaningful(v any) bool {
	switch val := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(val) != ""
	case []any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	}
	return true
}

// renderReceipt builds the user-facing message. Field order is fixed so the
// same outcome always arrives in the same shape, whichever agent produced it.
func renderReceipt(obj map[string]any) string {
	var b strings.Builder

	status, _ := obj["status"].(string)
	head := "**" + statusBadge(status) + "**"
	if id := firstString(obj, "identifier", "task_id"); id != "" {
		head += " · " + escapeMarkdownInline(id)
	}
	b.WriteString(head)

	for _, field := range []string{"agent", "summary"} {
		if value := fieldText(obj[field]); value != "" {
			fmt.Fprintf(&b, "\n**%s**: %s", strings.ToUpper(field), escapeMarkdownInline(value))
		}
	}
	if result := fieldText(obj["result"]); result != "" {
		fmt.Fprintf(&b, "\n\n**RESULT**\n%s", receiptBlock(result))
	}
	if verify := obj["verify"]; isMeaningful(verify) {
		fmt.Fprintf(&b, "\n\n**VERIFY**\n%s", renderVerify(verify))
	}
	if files := obj["files"]; isMeaningful(files) {
		fmt.Fprintf(&b, "\n\n**FILES**\n%s", renderList(files, "file"))
	}
	if next := fieldText(obj["next_step"]); next != "" {
		fmt.Fprintf(&b, "\n\n**NEXT_STEP**: %s", escapeMarkdownInline(next))
	}
	if details := renderDetails(obj); details != "" {
		fmt.Fprintf(&b, "\n\n%s", details)
	}
	return truncateRunes(redact.Text(b.String()), receiptTotalMaxRunes)
}

// statusBadge maps a receipt status onto a stable marker plus the status word
// itself, so the reader sees both a scannable glyph and the exact value.
func statusBadge(status string) string {
	glyph := "•"
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "completed", "success", "ok":
		glyph = "✅"
	case "in_review", "review", "verifying", "in_progress":
		glyph = "🔄"
	case "blocked", "waiting":
		glyph = "⛔"
	case "failed", "failure", "error":
		glyph = "❌"
	case "cancelled", "canceled":
		glyph = "🚫"
	}
	word := strings.TrimSpace(status)
	if word == "" {
		return glyph
	}
	return glyph + " " + word
}

// renderVerify turns the contract's verify array into one line per command:
// what ran, what it returned, and the evidence — the three things a reader
// checks before trusting the receipt.
func renderVerify(verify any) string {
	items, ok := verify.([]any)
	if !ok {
		return receiptBlock(fieldText(verify))
	}
	var lines []string
	for _, item := range items {
		switch v := item.(type) {
		case map[string]any:
			command, _ := v["command"].(string)
			evidence := fieldText(v["evidence"])
			line := "- `" + strings.TrimSpace(command) + "`"
			if code, ok := exitCodeText(v["exit_code"]); ok {
				line += " → exit " + code
			}
			if evidence != "" {
				line += " — " + escapeMarkdownInline(truncateRunes(evidence, 300))
			}
			if command == "" && evidence == "" {
				continue
			}
			lines = append(lines, line)
		default:
			if text := fieldText(v); text != "" {
				lines = append(lines, "- "+escapeMarkdownInline(text))
			}
		}
	}
	if len(lines) == 0 {
		return "_no verification recorded_"
	}
	return strings.Join(lines, "\n")
}

func exitCodeText(v any) (string, bool) {
	switch code := v.(type) {
	case float64:
		return strconv.FormatInt(int64(code), 10), true
	case string:
		if strings.TrimSpace(code) == "" {
			return "", false
		}
		return strings.TrimSpace(code), true
	case json.Number:
		return code.String(), true
	}
	return "", false
}

func renderList(value any, noun string) string {
	items, ok := value.([]any)
	if !ok {
		return "- " + escapeMarkdownInline(fieldText(value))
	}
	var lines []string
	for _, item := range items {
		if text := fieldText(item); text != "" {
			lines = append(lines, "- `"+escapeMarkdownInline(text)+"`")
		}
	}
	if len(lines) == 0 {
		return "- _no " + noun + "s_"
	}
	return strings.Join(lines, "\n")
}

// renderDetails emits the fields the contract does not name, minus the ones
// that are secret or internal. Insertion order is unstable for a Go map, so
// the keys are sorted — the same receipt must render the same way twice.
func renderDetails(obj map[string]any) string {
	known := make(map[string]bool, len(receiptFields))
	for _, field := range receiptFields {
		known[field] = true
	}
	var keys []string
	for key, value := range obj {
		if known[key] || droppedReceiptFields[strings.ToLower(key)] || isSensitiveField(key) {
			continue
		}
		if !isMeaningful(value) {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		lines = append(lines, "- **"+escapeMarkdownInline(key)+"**: "+escapeMarkdownInline(scalarText(obj[key])))
	}
	return "**DETAILS**\n" + strings.Join(lines, "\n")
}

func isSensitiveField(key string) bool {
	lower := strings.ToLower(key)
	for _, hint := range sensitiveFieldHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// fieldText flattens one JSON value to display text, keeping short lists on
// one line and refusing to dump a nested object as JSON.
func fieldText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(truncateRunes(stripStackTraces(v), receiptValueMaxRunes))
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case []any:
		var parts []string
		for _, item := range v {
			if text := scalarText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return truncateRunes(stripStackTraces(strings.Join(parts, ", ")), receiptValueMaxRunes)
	case map[string]any:
		return truncateRunes(stripStackTraces(scalarText(v)), receiptValueMaxRunes)
	}
	return ""
}

// stackTraceMarkers announce a runtime stack that followed the real error.
// The marker line and everything after it are dropped: the reader needs the
// reason, not the frames. Agents routinely append a Go/Python/Node trace to
// the same field as the cause, so a dedicated `stack` field is not enough.
var stackTraceMarkers = []string{
	"goroutine 1 [",
	"\nTraceback (most recent call last)",
	"\n  File \"",
	"\n    at ",
	"\npanic: ",
	"\nfatal error: ",
	"\nStack trace:",
}

// stripStackTraces cuts a raw runtime stack off the end of an error string,
// keeping whatever diagnosis came before it.
func stripStackTraces(text string) string {
	cut := -1
	for _, marker := range stackTraceMarkers {
		if i := strings.Index(text, marker); i >= 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if cut >= 0 {
		text = text[:cut]
	}
	// Drop a label that introduced the trace ("Stack:", "Traceback:"), which
	// is left dangling on the line above once the frames are gone.
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && isStackLabel(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// isStackLabel reports a line that was only ever a heading for the frames
// below it. The word must be the whole line, so a real message that happens to
// mention a stack is kept.
func isStackLabel(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "stack", "stack:", "stacktrace", "stacktrace:", "stack trace", "stack trace:",
		"traceback", "traceback:", "traceback (most recent call last):",
		"http trace:", "cause", "caused by:":
		return true
	}
	return false
}

// scalarText renders any value as one line, flattening nested objects to
// `k=v` pairs rather than re-emitting JSON.
func scalarText(value any) string {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+"="+scalarText(v[key]))
		}
		return strings.Join(parts, ", ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, scalarText(item))
		}
		return strings.Join(parts, ", ")
	}
	return fieldText(value)
}

// receiptBlock renders a multi-line field, dedenting so a result written with
// leading spaces does not collapse into a ragged column.
func receiptBlock(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	// A result that is already Markdown (headings, lists, fences) is passed
	// through so the channel's renderer can format it; only the leading indent
	// is normalized.
	indent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " "))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent > 0 {
		for i, line := range lines {
			if len(line) >= indent {
				lines[i] = line[indent:]
			}
		}
	}
	return strings.Join(lines, "\n")
}

func firstString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := obj[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// escapeMarkdownInline keeps a value from breaking the surrounding markup.
//
// Only the backtick is neutralized: an odd one would open a code span that
// swallows the rest of the line. Asterisks and underscores are left alone —
// every renderer here requires a non-space on both sides of an emphasis
// marker, so an identifier like `raw_output`, a path, or `2 * 3` cannot turn
// into markup, and rewriting them would corrupt the very values the reader is
// checking.
func escapeMarkdownInline(s string) string {
	return strings.ReplaceAll(s, "`", "'")
}

// truncateRunes cuts on a rune boundary and marks the cut, so the reader knows
// the receipt was shortened rather than ending mid-word by accident.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i] + "…"
		}
		count++
	}
	return s
}
