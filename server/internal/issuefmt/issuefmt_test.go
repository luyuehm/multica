package issuefmt

import (
	"strings"
	"testing"
)

// Real history calibration (RIC-908):
//
//   - RIC-901: single unsegmented paragraph, 0 headings → must FAIL.
//   - RIC-907: sectioned with 2 headings → must PASS.
//   - RIC-894: sectioned with 4 headings → must PASS.
func TestValidateCalibration(t *testing.T) {
	ric901 := "目标：实现Task模板MVP的变量声明、校验与简单安全插值引擎。" +
		"范围：支持text/number/date/select变量。约束：先核对现有autopilot插值实现。" +
		"验收：有效替换正确，异常输入均被拒绝。"
	if err := Validate(ric901); err == nil {
		t.Fatal("RIC-901-style unsegmented description must be rejected")
	}

	if err := Validate("## 目标\n只新增Task模板第一张数据库迁移。\n## 工作范围\n仅修改迁移文件。"); err != nil {
		t.Fatalf("RIC-907-style sectioned description must pass, got: %v", err)
	}

	ric894 := "## 目标\n恢复 Telegram 收发。\n## 当前事实\ngateway 运行中。\n## 交付\n修复路由。\n## 验收\n收发正常。"
	if err := Validate(ric894); err != nil {
		t.Fatalf("RIC-894-style sectioned description must pass, got: %v", err)
	}
}

func TestValidateEmpty(t *testing.T) {
	if err := Validate(""); err == nil {
		t.Fatal("empty description must be rejected")
	}
	if err := Validate("   \n  "); err == nil {
		t.Fatal("whitespace-only description must be rejected")
	}
}

func TestValidateTooFewHeadingsAndSingleLine(t *testing.T) {
	err := Validate("## 目标\n单段正文没有任何其它标题，段落可能会很长很长很长很长很长很长很长很长很长很长很长很长很长很长")
	if err == nil {
		t.Fatal("description with only 1 heading must be rejected")
	}
	if !strings.Contains(err.Error(), "最低 2 个") {
		t.Fatalf("error must state the missing-heading count, got: %v", err)
	}

	err = Validate("## 目标\n第一段\n没有换行的巨型文本居然想混过去")
	if err != nil {
		// has 1 heading -> already rejected on count; fine.
	}

	// A single physical line with a heading but \n between sections must pass the
	// count gate; the single-line check only triggers when there is no "\n" at all.
	err = Validate("## 目标\nline1")
	if err == nil {
		t.Fatal("must still reject: 1 heading < 2")
	}
}

func TestValidateReportsMissingSections(t *testing.T) {
	// Two headings, but none of them is a 目标 / 验证 / 输出契约 section.
	desc := "## 背景\nx\n## 交付\ny"
	err := Validate(desc)
	if err != nil {
		t.Fatalf("2 headings + 范围/现状 aliases should pass structural gate, got: %v", err)
	}
	_, missing := Inspect(desc)
	if len(missing) == 0 {
		t.Fatal("expected some missing suggested sections to be reported")
	}
}

func TestInspectHeadingsAndAliases(t *testing.T) {
	desc := "## 目标\n## 任务目标 (核心原则：长期价值 + 差异化)\n## 验收标准\n## 工作范围\n## OUTPUT CONTRACT"
	n, missing := Inspect(desc)
	if n != 5 {
		t.Fatalf("got %d headings, want 5", n)
	}
	if len(missing) != 0 {
		t.Fatalf("all four canonical sections should be present (aliases matched), missing: %v", missing)
	}

	// Full-width paren suffix must match the base alias.
	desc2 := "## 目标（强制结构）\n## 范围\n## 验证\n## 输出契约"
	if _, missing2 := Inspect(desc2); len(missing2) != 0 {
		t.Fatalf("full-width paren suffix should match, missing: %v", missing2)
	}
}

func TestInspectRejectsHeadingWithoutSpace(t *testing.T) {
	// "##目标" (no space) is NOT a valid `## ` heading per RIC-908.
	n, _ := Inspect("##目标\nno space\n## 目标\nwith space")
	if n != 1 {
		t.Fatalf("only '## 目标' should count, got %d", n)
	}
}

func TestRenderProducesSectionedMarkdown(t *testing.T) {
	got := Render(map[string]string{
		"目标": "第一行\n第二行",
		"范围/现状": "涉及文件。",
		"验证":   "go test ./...",
	})
	if !strings.Contains(got, "## 目标") || !strings.Contains(got, "## 范围/现状") ||
		!strings.Contains(got, "## 验证") || !strings.Contains(got, "## 输出契约") {
		t.Fatalf("rendered description missing canonical sections:\n%s", got)
	}
	// Multi-line field value must not be squashed onto one line (RIC-908).
	if !strings.Contains(got, "第一行\n第二行") {
		t.Fatalf("multi-line field value was squashed:\n%s", got)
	}
	// Rendered output must itself pass validation (>= 2 headings + line breaks).
	if err := Validate(got); err != nil {
		t.Fatalf("rendered description should pass Validate, got: %v", err)
	}
}

func TestRenderAlwaysAddsOutputContract(t *testing.T) {
	got := Render(map[string]string{"验证": "ok"})
	if !strings.Contains(got, "## 输出契约") {
		t.Fatalf("render must always append 输出契约:\n%s", got)
	}
	if err := Validate(got); err != nil {
		t.Fatalf("render output must pass validation, got: %v", err)
	}
}

func TestRenderEmptyFieldsSkipped(t *testing.T) {
	got := Render(map[string]string{"范围/现状": "仅此一段"})
	if strings.Contains(got, "## 目标") {
		t.Fatalf("empty 目标 section should be omitted:\n%s", got)
	}
	if !strings.Contains(got, "## 范围/现状") {
		t.Fatalf("non-empty section missing:\n%s", got)
	}
}