package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// telegramDelivery records what a delivery actually put on the wire. The tests
// below assert on this rather than on the formatter's return value, because the
// failure the issue reports is what the USER reads: a chunk whose HTML Telegram
// rejects is re-sent as plain text, so a formatting bug is invisible in
// formatHTML's own output and obvious only in the final sendMessage body.
type telegramDelivery struct {
	mu       sync.Mutex
	requests []telegramWireRequest
}

type telegramWireRequest struct {
	method   string
	text     string
	parseMod string
}

func (d *telegramDelivery) lastText() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) == 0 {
		return ""
	}
	return d.requests[len(d.requests)-1].text
}

func (d *telegramDelivery) allText() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.requests))
	for _, r := range d.requests {
		out = append(out, r.text)
	}
	return out
}

func (d *telegramDelivery) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

// sentAsPlainText reports whether any sendMessage dropped the HTML parse mode,
// which happens only when Telegram refuses the markup we produced.
func (d *telegramDelivery) sentAsPlainText() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var plain []string
	for _, r := range d.requests {
		if r.method == "sendMessage" && r.parseMod != "HTML" {
			plain = append(plain, r.text)
		}
	}
	return plain
}

// newReceiptDeliveryHarness wires an Outbound against a fake Telegram that
// accepts everything and records the bodies.
func newReceiptDeliveryHarness(t *testing.T) (*Outbound, *telegramDelivery) {
	t.Helper()
	delivery := &telegramDelivery{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode %s request: %v", r.URL.Path, err)
			http.Error(w, "bad test request", http.StatusBadRequest)
			return
		}
		text, _ := body["text"].(string)
		parseMode, _ := body["parse_mode"].(string)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		delivery.mu.Lock()
		delivery.requests = append(delivery.requests, telegramWireRequest{method: method, text: text, parseMod: parseMode})
		delivery.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if method == "sendMessage" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99,"chat":{"id":42,"type":"private"},"date":0,"text":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(api.Close)
	q := newTelegramOutboundQueries(t)
	q.channelOrigin = true
	return NewOutbound(q, nil, api.URL, api.Client(), nil), delivery
}

// deliverReceipt runs one completion through the real terminal queue.
func deliverReceipt(t *testing.T, o *Outbound, content string) {
	t.Helper()
	e := telegramTestEventFor(3, 2, content)
	if err := sendTerminalReplySynchronouslyForTest(context.Background(), o, e); err != nil {
		t.Fatalf("deliver reply: %v", err)
	}
}

// A machine-readable receipt must arrive as readable text, not as the JSON the
// agent emitted. This is the issue's headline acceptance criterion.
func TestReceiptDeliveryRendersInsteadOfRawJSON(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    []string
	}{
		{
			name: "success",
			content: `{"task_id":"11111111-1111-4111-8111-111111111111","status":"done",` +
				`"agent":"claude-code-agent","summary":"修复回执格式化","result":"重写渲染","next_step":"合并"}`,
			want: []string{"✅ done", "<b>SUMMARY</b>", "修复回执格式化", "<b>NEXT_STEP</b>", "合并"},
		},
		{
			name:    "blocked",
			content: `{"task_id":"abc","status":"blocked","summary":"等待授权","next_step":"@luyuehm 确认"}`,
			want:    []string{"⛔ blocked", "<b>SUMMARY</b>", "等待授权"},
		},
		{
			name:    "failed",
			content: `{"task_id":"abc","status":"failed","summary":"run failed","result":"compile error"}`,
			want:    []string{"❌ failed", "run failed", "compile error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, delivery := newReceiptDeliveryHarness(t)
			deliverReceipt(t, o, tc.content)

			text := delivery.lastText()
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("delivered text missing %q:\n%s", want, text)
				}
			}
			// The receipt's own JSON syntax must be gone — that is the whole bug.
			for _, unwanted := range []string{`"task_id"`, `"status"`, `{"`, `"summary"`} {
				if strings.Contains(text, unwanted) {
					t.Errorf("delivered text still carries raw JSON %q:\n%s", unwanted, text)
				}
			}
			// It must go out as HTML: a plain-text send means Telegram rejected
			// our markup, which is the silent-formatting-loss path.
			if plain := delivery.sentAsPlainText(); len(plain) > 0 {
				t.Errorf("Telegram refused the markup and the reply fell back to plain text:\n%s",
					strings.Join(plain, "\n---\n"))
			}
		})
	}
}

// Chinese text, backticks, code fences, links and special characters must
// survive the round trip. Before the formatter rewrite an unbalanced tag here
// made Telegram reject the chunk, and the plain-text fallback showed every
// marker to the user.
func TestReceiptDeliveryKeepsMarkdownAndChinese(t *testing.T) {
	content := "## ✅ 完成\n\n" +
		"**SUMMARY**: 修复 `markdown` 渲染 & 分片\n\n" +
		"**RESULT**:\n- 中文链接：[文档](https://例え.jp/路径?q=值&x=1)\n" +
		"- 特殊字符：a < b & c > d\n" +
		"- 代码：`go test ./...`\n\n" +
		"```go\nfmt.Println(\"x < 1 & y\")\n```\n\n" +
		"**NEXT_STEP**: 合并 PR"

	o, delivery := newReceiptDeliveryHarness(t)
	deliverReceipt(t, o, content)

	text := delivery.lastText()
	for _, want := range []string{
		"<b>✅ 完成</b>",
		"<code>markdown</code>",
		"&amp; 分片",
		`<a href="https://例え.jp/路径?q=值&amp;x=1">文档</a>`,
		"a &lt; b &amp; c &gt; d",
		`<pre><code class="language-go">fmt.Println(&#34;x &lt; 1 &amp; y&#34;)</code></pre>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("delivered text missing %q:\n%s", want, text)
		}
	}
	if defect := tagBalance(text); defect != "" {
		t.Errorf("delivered HTML is unbalanced (%s):\n%s", defect, text)
	}
	if plain := delivery.sentAsPlainText(); len(plain) > 0 {
		t.Errorf("reply fell back to plain text:\n%s", strings.Join(plain, "\n---\n"))
	}
}

// A long reply must arrive as several well-formed chunks, every one of them
// valid Telegram HTML — including the pieces a code fence was split across.
func TestReceiptDeliveryChunksLongTextSafely(t *testing.T) {
	var b strings.Builder
	b.WriteString("**SUMMARY**: 长回复分片测试\n\n")
	b.WriteString("```go\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "line%03d := compute(%d) // **marker** `tick`\n", i, i)
	}
	b.WriteString("```\n")

	o, delivery := newReceiptDeliveryHarness(t)
	deliverReceipt(t, o, b.String())

	if delivery.count() < 2 {
		t.Fatalf("expected the reply to split, got %d message(s)", delivery.count())
	}
	for i, text := range delivery.allText() {
		if defect := tagBalance(text); defect != "" {
			t.Errorf("chunk %d unbalanced (%s):\n%s", i, defect, text)
		}
		// Each piece must show its code as code, not as stray backticks.
		if strings.Count(text, "<pre>") != strings.Count(text, "</pre>") {
			t.Errorf("chunk %d has an unbalanced code fence:\n%s", i, text)
		}
		if strings.Contains(text, "```") {
			t.Errorf("chunk %d leaked the literal fence marker:\n%.200s", i, text)
		}
	}
	if plain := delivery.sentAsPlainText(); len(plain) > 0 {
		t.Errorf("%d chunk(s) fell back to plain text", len(plain))
	}
}

// An empty completion is not a receipt and must not be turned into one: the
// adapter treats empty content as "close the turn, say nothing".
func TestReceiptDeliveryLeavesEmptyContentAlone(t *testing.T) {
	if got := chatDoneContent(protocol.ChatDonePayload{Content: ""}); got != "" {
		t.Errorf("empty content became %q", got)
	}
	// Malformed or unrelated JSON is not a receipt either.
	for _, in := range []string{"{}", "   ", "{", `{"status":`, `{"status":"ok","results":[{"id":1}]}`} {
		if got := chatDoneContent(protocol.ChatDonePayload{Content: in}); got != in {
			t.Errorf("chatDoneContent(%q) = %q, want passthrough", in, got)
		}
	}
}

// The map form of the payload (after a serialization round trip across
// replicas) must normalize the same way as the typed one — otherwise a reply
// posted through one server replica reads differently from the other.
func TestReceiptDeliveryNormalizesMapPayload(t *testing.T) {
	content := `{"status":"done","summary":"map form","next_step":"ship"}`
	typed := chatDoneContent(protocol.ChatDonePayload{Content: content})
	mapped := chatDoneContent(map[string]any{"content": content})
	if typed == content {
		t.Fatal("typed payload was not normalized")
	}
	if typed != mapped {
		t.Fatalf("typed and map forms disagree:\ntyped:  %q\nmapped: %q", typed, mapped)
	}
}
