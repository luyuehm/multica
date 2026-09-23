// Package issuefmt enforces a minimum structured-markdown shape on issue
// descriptions created through the multica CLI. Every `multica issue create`
// passes descriptions through Validate; automation scripts that shell out to
// `multica issue create` inherit the gate for free. Render exports the single
// template-rendering entry so callers build sectioned descriptions instead of
// hand-formatting them (RIC-908).
//
// The gate is deliberately structural rather than prose-lenient: there is no
// cap on length, only a requirement that the body is Markdown sectioned by
// `## ` headings with real line breaks. The threshold is calibrated against
// real history so legitimate short tasks keep passing:
//
//   - RIC-901 (the regression this gate exists to block): 0 `## ` headings,
//     a single unsegmented paragraph → rejected.
//   - RIC-907 / RIC-894: sectioned bodies (2 / 4 headings) → pass.
package issuefmt

import (
	"fmt"
	"regexp"
	"strings"
)

// MinHeadings is the minimum number of `## ` headings a description must
// carry. 2 is the calibrated boundary: RIC-907 has 2 headings and must keep
// passing, RIC-901 has 0 and must be rejected.
const MinHeadings = 2

// Section describes one canonical task-description section. Name is how the
// section is reported when missing; Aliases are the accepted heading wordings
// (matched case-insensitively, allowing parenthetical suffixes like
// "任务目标 (核心原则：...)").
type Section struct {
	Name    string
	Aliases []string
}

// SuggestedSections are the sections the issue template (RIC-908) wants every
// task to carry. They are the reported-missing set in validation errors. Only
// the heading count + line structure is a hard gate, so legacy but sectioned
// issues do not regress, while the guidance steers creators toward the full
// structure.
var SuggestedSections = []Section{
	{Name: "目标", Aliases: []string{"目标", "任务目标", "解决的问题"}},
	{Name: "范围/现状", Aliases: []string{"范围", "工作范围", "现状", "背景", "当前事实", "评估发现", "交付", "来源"}},
	{Name: "验证", Aliases: []string{"验证", "验收", "验收标准", "测试", "构建/测试/验证命令", "验收口径"}},
	{Name: "输出契约", Aliases: []string{"输出契约", "output contract", "输出 contract"}},
}

// headingRe matches a Markdown `## ` heading line. It requires at least one
// space after `##` and a non-empty heading text, per the "`## ` headers"
// contract in RIC-908.
var headingRe = regexp.MustCompile(`(?m)^##[ \t]+([^\n]+?)[ \t]*$`)

// Inspect counts `## ` headings and returns the canonical sections absent
// from desc. It is the shared structural analyzer used by Validate and by the
// Python helper (which mirrors this implementation).
func Inspect(desc string) (headings int, missing []string) {
	body := strings.ReplaceAll(desc, "\r\n", "\n")
	headingTexts := make([]string, 0)
	for _, m := range headingRe.FindAllStringSubmatch(body, -1) {
		headingTexts = append(headingTexts, strings.ToLower(strings.TrimSpace(m[1])))
	}
	headings = len(headingTexts)

	for _, sec := range SuggestedSections {
		found := false
		for _, h := range headingTexts {
			if aliasMatches(h, sec.Aliases) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, "## "+sec.Name)
		}
	}
	return headings, missing
}

// aliasMatches reports whether heading text h carries alias a, either exactly
// or as a prefix followed by a separator (half or full-width space, "(" or
// "（"), e.g. "任务目标 (核心原则...)" matches alias "任务目标".
func aliasMatches(h string, aliases []string) bool {
	for _, a := range aliases {
		low := strings.ToLower(a)
		if h == low {
			return true
		}
		separators := []string{low + " ", low + "(", low + "（"}
		for _, sep := range separators {
			if strings.HasPrefix(h, sep) {
				return true
			}
		}
	}
	return false
}

// Validate checks desc for the minimum structured-markdown shape and returns
// an actionable error naming what is missing, or nil when the description
// passes. Empty descriptions are rejected (a task with no prose cannot be
// inspected); so are single-line bodies that never introduced a line break.
func Validate(desc string) error {
	if strings.TrimSpace(desc) == "" {
		return fmt.Errorf(
			"description 不能为空：强制结构校验要求至少 %d 个 `## ` 标题，且应包含“%s”等段。",
			MinHeadings, strings.Join(recommendedNames(), "、"))
	}
	headings, missing := Inspect(desc)
	if headings < MinHeadings {
		return fmt.Errorf(
			"description 未通过强制结构校验：仅含 %d 个 `## ` 标题（最低 %d 个）；缺失段：%s。\n"+
				"请使用多行 Markdown（`--description-file` / `--description-stdin`）分段编写，参考模板：\n%s",
			headings, MinHeadings, missingOrNone(missing), renderExample())
	}
	// Newline-structure sanity: a body with headings but no line breaks at all
	// (e.g. one giant line) still cannot be inspected reliably.
	if !strings.Contains(desc, "\n") {
		return fmt.Errorf(
			"description 是单行文本、缺少换行分段；强制结构校验要求按 `## ` 标题分行书写。缺失段：%s。",
			missingOrNone(missing))
	}
	return nil
}

// Render builds a standard sectioned Markdown description from optional
// structured fields. Each non-empty field becomes its own `## ` section in
// canonical order; the 输出契约 section is always appended so every rendered
// issue carries one. Field values are preserved verbatim (multi-line content
// stays multi-line) — never squashed onto a single line (RIC-908).
func Render(fields map[string]string) string {
	if fields == nil {
		fields = map[string]string{}
	}
	order := []string{"目标", "范围/现状", "验证"}
	var b strings.Builder
	for _, key := range order {
		value := strings.TrimSpace(fields[key])
		if value == "" {
			continue
		}
		b.WriteString("\n## " + key + "\n" + value + "\n")
	}
	oc := strings.TrimSpace(fields["输出契约"])
	if oc == "" {
		oc = `{"task_id":"<KEY>","status":"done|blocked|failed","agent":"<agent>","summary":"(≤200字)","result":"执行结果/验证证据","next":"下一步"}`
	}
	b.WriteString("\n## 输出契约\n" + oc + "\n")
	return strings.TrimSpace(b.String()) + "\n"
}

func recommendedNames() []string {
	out := make([]string, 0, len(SuggestedSections))
	for _, s := range SuggestedSections {
		out = append(out, "## "+s.Name)
	}
	return out
}

func missingOrNone(missing []string) string {
	if len(missing) == 0 {
		return "无（但 ## 标题数量不足）"
	}
	return strings.Join(missing, "、")
}

func renderExample() string {
	return Render(map[string]string{
		"目标":   "一句话说明要交付什么、验收口径是什么。",
		"范围/现状": "涉及文件、影响范围、当前现状。",
		"验证":   "可量化的验收命令与通过条件。",
		"输出契约": `{"task_id":"RIC-xxx","status":"done|blocked|failed","agent":"agent名称","summary":"(≤200字)","result":"执行结果/验证证据","next":"下一步建议"}`,
	})
}