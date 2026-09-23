package handler

import (
	"fmt"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestCreateAutopilotRoundTripsIssueBodyTemplate verifies the create API
// accepts issue_body_template, stores it, and returns it in the response.
func TestCreateAutopilotRoundTripsIssueBodyTemplate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	title := fmt.Sprintf("body-template autopilot %d", time.Now().UnixNano())
	var agentID string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID)

	bodyTmpl := "## 目标\n{{description}}\n\n## 日期\n{{date}}"
	req := newRequest("POST", "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":               title,
		"assignee_id":         agentID,
		"execution_mode":      "run_only",
		"issue_body_template": bodyTmpl,
	})

	w := testutil.Call(t, testHandler.CreateAutopilot, req).Want(200)
	var resp AutopilotResponse
	w.JSON(&resp)

	if resp.IssueBodyTemplate == nil {
		t.Fatal("IssueBodyTemplate is nil in response, want the stored template")
	}
	if *resp.IssueBodyTemplate != bodyTmpl {
		t.Fatalf("IssueBodyTemplate = %q, want %q", *resp.IssueBodyTemplate, bodyTmpl)
	}
}

// TestCreateAutopilotRejectsInvalidIssueBodyTemplate verifies an unknown
// {{...}} token in the body template is rejected at create time with a
// clear error.
func TestCreateAutopilotRejectsInvalidIssueBodyTemplate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	var agentID string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID)

	req := newRequest("POST", "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":               fmt.Sprintf("bad-body-template %d", time.Now().UnixNano()),
		"assignee_id":         agentID,
		"execution_mode":      "run_only",
		"issue_body_template": "## 目标\n{{bogus_var}}",
	})

	w := testutil.Call(t, testHandler.CreateAutopilot, req).Want(400)
	body := w.Body.String()
	// The handler surfaces the validator error verbatim.
	if !bodyContains(body, "unknown body template variable") || !bodyContains(body, "bogus_var") {
		t.Fatalf("rejection body should name the offending token: %s", body)
	}
}

// TestUpdateAutopilotRoundTripsIssueBodyTemplate verifies the update API can
// set issue_body_template (and that an absent field leaves it unchanged).
func TestUpdateAutopilotRoundTripsIssueBodyTemplate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	title := fmt.Sprintf("update-body-template autopilot %d", time.Now().UnixNano())
	var agentID string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID)

	// Create without a body template.
	req := newRequest("POST", "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":          title,
		"assignee_id":    agentID,
		"execution_mode": "run_only",
	})
	w := testutil.Call(t, testHandler.CreateAutopilot, req).Want(200)
	var created AutopilotResponse
	w.JSON(&created)
	if created.IssueBodyTemplate != nil {
		t.Fatalf("create without body template should leave it nil, got %q", *created.IssueBodyTemplate)
	}

	bodyTmpl := "## 目标\n{{description}}"
	req = newRequest("PUT", "/api/autopilots/"+created.ID, map[string]any{
		"issue_body_template": bodyTmpl,
	})
	req = withURLParam(req, "id", created.ID)
	w = testutil.Call(t, testHandler.UpdateAutopilot, req).Want(200)
	var updated AutopilotResponse
	w.JSON(&updated)
	if updated.IssueBodyTemplate == nil || *updated.IssueBodyTemplate != bodyTmpl {
		t.Fatalf("updated IssueBodyTemplate = %v, want %q", updated.IssueBodyTemplate, bodyTmpl)
	}
}

// TestAutopilotTriggerCreateIssueAppliesBodyTemplate is the end-to-end smoke at
// the handler boundary: triggering a create_issue autopilot with a body
// template must produce an issue whose body carries the template sections.
func TestAutopilotTriggerCreateIssueAppliesBodyTemplate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	title := fmt.Sprintf("trigger-body-template autopilot %d", time.Now().UnixNano())
	var agentID string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID)

	bodyTmpl := "## 目标\n{{description}}\n\n## 日期\n{{date}}"
	req := newRequest("POST", "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":                title,
		"assignee_id":          agentID,
		"execution_mode":       "create_issue",
		"issue_title_template": title,
		"issue_body_template":  bodyTmpl,
	})
	w := testutil.Call(t, testHandler.CreateAutopilot, req).Want(200)
	var autopilot AutopilotResponse
	w.JSON(&autopilot)

	req = newRequest("POST", "/api/autopilots/"+autopilot.ID+"/trigger?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", autopilot.ID)
	w = testutil.Call(t, testHandler.TriggerAutopilot, req).Want(200)
	var run AutopilotRunResponse
	w.JSON(&run)
	if run.IssueID == nil {
		t.Fatal("run issue_id is nil, want created issue")
	}

	// The created issue's body must contain the template sections and the
	// interpolated description.
	var body string
	dbfx.QueryRow(t, `SELECT description FROM issue WHERE id = $1`, *run.IssueID).Scan(&body)
	if !bodyContains(body, "## 目标") || !bodyContains(body, "## 日期") {
		t.Fatalf("created issue body should contain template sections, got: %q", body)
	}
}

// bodyContains reports whether s contains the substring sub. Named distinctly
// from the package-level containsString ([]string) helper in skill_test.go to
// avoid a redeclaration in the same test package.
func bodyContains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
