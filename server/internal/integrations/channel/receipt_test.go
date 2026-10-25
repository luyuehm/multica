package channel

import (
	"encoding/json"
	"strings"
	"testing"
)

// A receipt is the machine-readable JSON an agent sometimes finishes with. It
// used to be forwarded to Telegram verbatim, so the user read a raw JSON wall.
// These pin the rendering contract the issue asks for: stable field order,
// stable status marker, no credentials, no process bookkeeping.
func TestNormalizeAgentTextRendersReceipt(t *testing.T) {
	const body = `{"task_id":"11111111-1111-4111-8111-111111111111",` +
		`"status":"done","agent":"claude-code-agent","summary":"修复回执格式化",` +
		`"result":"重写 markdown 渲染\n修复分片",` +
		`"files":["server/internal/integrations/telegram/markdown.go"],` +
		`"verify":[{"command":"go test ./internal/integrations/telegram/","exit_code":0,"evidence":"ok 0.58s"}],` +
		`"next_step":"合并","context_rule":"完整证据留台账"}`

	got := NormalizeAgentText(body)

	for _, want := range []string{
		"**✅ done**",
		"11111111-1111-4111-8111-111111111111",
		"**AGENT**: claude-code-agent",
		"**SUMMARY**: 修复回执格式化",
		"**RESULT**",
		"**VERIFY**",
		"- `go test ./internal/integrations/telegram/` → exit 0 — ok 0.58s",
		"**FILES**",
		"- `server/internal/integrations/telegram/markdown.go`",
		"**NEXT_STEP**: 合并",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered receipt missing %q:\n%s", want, got)
		}
	}
	// The contract's own instructions to the reader are not part of the
	// message; nor may the raw JSON survive.
	for _, unwanted := range []string{"context_rule", `"task_id"`, `"summary"`, "{"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("rendered receipt still contains %q:\n%s", unwanted, got)
		}
	}
	// Field order is fixed so the same outcome always arrives the same shape.
	if strings.Index(got, "RESULT") > strings.Index(got, "NEXT_STEP") {
		t.Errorf("RESULT must precede NEXT_STEP:\n%s", got)
	}
}

// Each terminal state gets a distinct, stable marker. The issue requires
// success / failed / blocked to be tellable apart at a glance.
func TestNormalizeAgentTextStatusMarkers(t *testing.T) {
	for _, tc := range []struct{ status, marker string }{
		{"done", "✅"},
		{"completed", "✅"},
		{"in_review", "🔄"},
		{"in_progress", "🔄"},
		{"blocked", "⛔"},
		{"failed", "❌"},
		{"cancelled", "🚫"},
		{"some_new_status", "•"},
	} {
		got := NormalizeAgentText(`{"status":"` + tc.status + `","summary":"s"}`)
		if !strings.Contains(got, tc.marker) {
			t.Errorf("status %q rendered %q without marker %q", tc.status, got, tc.marker)
		}
		if !strings.Contains(got, tc.status) {
			t.Errorf("status %q rendered %q without the status word", tc.status, got)
		}
	}
}

// Credentials and stack traces are the two things the issue calls out
// explicitly. Neither may reach the user even when the agent puts it in a
// field the contract names.
func TestNormalizeAgentTextDropsSecretsAndProcessNoise(t *testing.T) {
	// A synthetic token with the shape the redactor matches; no real credential.
	const fakeToken = "sk-TESTFIXTUREnotarealkey000000000000"
	body := `{"status":"failed","summary":"run failed",` +
		`"result":"Error: auth Bearer ` + fakeToken + ` rejected",` +
		`"debug":"goroutine 1 [running]:\nmain.main()",` +
		`"api_token":"plain-secret-value","stack":"at foo()",` +
		`"duration_ms":1200,"model":"deepseek-v4"}`

	got := NormalizeAgentText(body)

	for _, leaked := range []string{
		fakeToken,
		"goroutine 1",
		"main.main()",
		"plain-secret-value",
		"at foo()",
		"api_token",
	} {
		if strings.Contains(got, leaked) {
			t.Errorf("leaked %q:\n%s", leaked, got)
		}
	}
	// The cause and non-sensitive extras must survive: the user still needs to
	// know why it failed and what ran.
	for _, want := range []string{"run failed", "REDACTED API KEY", "duration_ms", "deepseek-v4"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

// Text that is not exactly one receipt object must pass through untouched.
// An agent asked to return JSON gets to return JSON.
func TestNormalizeAgentTextLeavesNonReceiptsAlone(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"prose quoting json", `Here is the result: {"status":"done"} — all good.`},
		{"arbitrary json document", `{"status":"ok","results":[{"id":1}]}`},
		{"single field", `{"status":"ok"}`},
		{"summary only", `{"summary":"just a summary"}`},
		{"array not object", `[{"status":"done","summary":"x"}]`},
		{"empty", ""},
		{"plain markdown", "## Done\n\n**RESULT**: ok"},
	} {
		if got := NormalizeAgentText(tc.in); got != tc.in {
			t.Errorf("%s: expected passthrough, got %q", tc.name, got)
		}
	}
}

// Long or structurally broken receipts must still produce a bounded, readable
// message — the channel's own chunker handles anything past the cap.
func TestNormalizeAgentTextBoundsOutput(t *testing.T) {
	long := strings.Repeat("很长的结果行。\n", 2000)
	body := `{"status":"done","summary":"s","result":"` + strings.ReplaceAll(long, "\n", `\n`) + `"}`
	got := NormalizeAgentText(body)
	if len([]rune(got)) > receiptTotalMaxRunes {
		t.Fatalf("rendered %d runes, cap is %d", len([]rune(got)), receiptTotalMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated receipt must say so:\n%.80s", got)
	}
}

// A runtime stack appended to the same field as the cause must be cut, and the
// cause must survive. Agents routinely put "Error: X\nStack: goroutine 1…" in
// `result`; the reader needs the first line, not the frames.
func TestNormalizeAgentTextStripsInlineStackTraces(t *testing.T) {
	for _, tc := range []struct{ name, trace string }{
		{"go stack", "Stack: goroutine 1 [running]:\nmain.main()\n\t/app/main.go:10 +0x20"},
		{"go panic", "panic: runtime error: index out of range"},
		{"python traceback", "Traceback (most recent call last):\n  File app.py, line 1, in module"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"status":  "failed",
				"summary": "boom",
				"result":  "compile error: undefined: strings\n" + tc.trace,
			})
			if err != nil {
				t.Fatal(err)
			}
			got := NormalizeAgentText(string(payload))
			if !strings.Contains(got, "compile error: undefined: strings") {
				t.Errorf("the cause was dropped:\n%s", got)
			}
			for _, leaked := range []string{"goroutine 1", "main.main()", "Traceback (most recent call last)", "panic: runtime error"} {
				if strings.Contains(got, leaked) {
					t.Errorf("stack frame %q leaked:\n%s", leaked, got)
				}
			}
			if strings.Contains(got, "Stack:") {
				t.Errorf("dangling stack label left behind:\n%s", got)
			}
		})
	}
}

// Ordinary prose that merely mentions a stack is not a trace and must survive.
func TestNormalizeAgentTextKeepsProseAboutStacks(t *testing.T) {
	body := `{"status":"failed","summary":"s","result":"The stack is empty when the queue drains."}`
	if got := NormalizeAgentText(body); !strings.Contains(got, "The stack is empty when the queue drains.") {
		t.Errorf("prose was mistaken for a trace:\n%s", got)
	}
}
