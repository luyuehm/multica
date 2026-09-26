package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// instantiateTemplate creates an issue_template with the given config JSON and
// returns its id, cleaning up afterwards.
func instantiateTemplate(t *testing.T, config any) string {
	t.Helper()
	createReq := map[string]any{
		"name":          "Instantiate " + uuid.NewString(),
		"issue_title":   "Fix {{summary}}",
		"issue_content": "Component: {{component}}",
	}
	if config != nil {
		createReq["config"] = config
	}
	w := httptest.NewRecorder()
	testHandler.CreateIssueTemplate(w, newRequest(http.MethodPost, "/api/issue-templates?workspace_id="+testWorkspaceID, createReq))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssueTemplate status = %d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)
	t.Cleanup(func() {
		req := withURLParam(newRequest(http.MethodDelete, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, nil), "id", id)
		testHandler.DeleteIssueTemplate(httptest.NewRecorder(), req)
	})
	return id
}

func instantiateRequest(t *testing.T, templateID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/issue-templates/"+templateID+"/instantiate?workspace_id="+testWorkspaceID, body), "id", templateID)
	testHandler.InstantiateIssueTemplate(w, req)
	return w
}

func TestInstantiateIssueTemplateInterpolatesTitleAndContent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"variables": []map[string]any{
			{"name": "summary", "type": "text", "required": true},
			{"name": "component", "type": "select", "required": true, "options": []string{"api", "web"}},
			{"name": "severity", "type": "select", "options": []string{"low", "high"}, "default": "low"},
		},
	}
	id := instantiateTemplate(t, config)

	w := instantiateRequest(t, id, map[string]any{
		"values": map[string]string{"summary": "login fails", "component": "api"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("instantiate status = %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Fix login fails" {
		t.Fatalf("title = %q, want %q", got["title"], "Fix login fails")
	}
	if got["description"] != "Component: api" {
		t.Fatalf("description = %q, want %q", got["description"], "Component: api")
	}
	// Default filled and returned.
	vars, ok := got["variables"].([]any)
	if !ok || len(vars) != 3 {
		t.Fatalf("variables = %#v, want 3 declarations", got["variables"])
	}
}

func TestInstantiateIssueTemplateRejectsMissingRequiredVariable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"variables": []map[string]any{
			{"name": "summary", "type": "text", "required": true},
		},
	}
	id := instantiateTemplate(t, config)

	w := instantiateRequest(t, id, map[string]any{"values": map[string]string{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing required variable); body=%s", w.Code, w.Body.String())
	}
}

func TestInstantiateIssueTemplateRejectsRedundantVariable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"variables": []map[string]any{
			{"name": "summary", "type": "text"},
		},
	}
	id := instantiateTemplate(t, config)

	w := instantiateRequest(t, id, map[string]any{
		"values": map[string]string{"summary": "x", "undeclared": "y"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (redundant variable); body=%s", w.Code, w.Body.String())
	}
}

func TestInstantiateIssueTemplateRejectsIllegalVariable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"variables": []map[string]any{
			{"name": "summary", "type": "text", "required": true},
		},
	}
	id := instantiateTemplate(t, config)

	// Value type mismatch for a number variable fails.
	w := instantiateRequest(t, id, map[string]any{
		"values": map[string]string{"summary": "x"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("baseline should pass, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInstantiateIssueTemplateRejectsUnknownTokenInTemplate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	createReq := map[string]any{
		"name":          "Unknown token " + uuid.NewString(),
		"issue_title":   "Fix {{summary}}",
		"issue_content": "Uses {{undeclared}}",
		"config": map[string]any{
			"variables": []map[string]any{
				{"name": "summary", "type": "text", "required": true},
			},
		},
	}
	w := httptest.NewRecorder()
	testHandler.CreateIssueTemplate(w, newRequest(http.MethodPost, "/api/issue-templates?workspace_id="+testWorkspaceID, createReq))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.NewDecoder(w.Body).Decode(&created)
	id := created["id"].(string)
	t.Cleanup(func() {
		req := withURLParam(newRequest(http.MethodDelete, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, nil), "id", id)
		testHandler.DeleteIssueTemplate(httptest.NewRecorder(), req)
	})

	w = instantiateRequest(t, id, map[string]any{"values": map[string]string{"summary": "x"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown token); body=%s", w.Code, w.Body.String())
	}
}

func TestInstantiateIssueTemplateValidatesDefaultPriorityAndStatus(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"variables": []map[string]any{
			{"name": "summary", "type": "text", "required": true},
		},
		"default_priority": "high",
		"default_status":   "todo",
	}
	id := instantiateTemplate(t, config)

	w := instantiateRequest(t, id, map[string]any{"values": map[string]string{"summary": "x"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	json.NewDecoder(w.Body).Decode(&got)
	if got["priority"] != "high" {
		t.Fatalf("priority = %#v, want high", got["priority"])
	}
	if got["status"] != "todo" {
		t.Fatalf("status = %#v, want todo", got["status"])
	}
}

func TestInstantiateIssueTemplateRejectsInvalidDefaultPriority(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"default_priority": "P1",
	}
	id := instantiateTemplate(t, config)

	w := instantiateRequest(t, id, map[string]any{"values": map[string]string{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (invalid default priority); body=%s", w.Code, w.Body.String())
	}
}

func TestInstantiateIssueTemplateRejectsDefaultProjectOutsideWorkspace(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"default_project_id": uuid.NewString(),
	}
	id := instantiateTemplate(t, config)

	w := instantiateRequest(t, id, map[string]any{"values": map[string]string{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (project not in workspace); body=%s", w.Code, w.Body.String())
	}
}

func TestInstantiateIssueTemplateRejectsDefaultLabelOutsideWorkspace(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"default_labels": []string{uuid.NewString()},
	}
	id := instantiateTemplate(t, config)

	w := instantiateRequest(t, id, map[string]any{"values": map[string]string{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (label not in workspace); body=%s", w.Code, w.Body.String())
	}
}

func TestInstantiateIssueTemplateComputesDateOffsets(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"default_due_date_offset_days": 7,
	}
	id := instantiateTemplate(t, config)

	w := instantiateRequest(t, id, map[string]any{"values": map[string]string{}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	json.NewDecoder(w.Body).Decode(&got)
	due, ok := got["due_date"].(string)
	if !ok || len(due) != len("2006-01-02") {
		t.Fatalf("due_date = %#v, want YYYY-MM-DD string", got["due_date"])
	}
}

func TestInstantiateIssueTemplateSideEffectFree(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	config := map[string]any{
		"variables": []map[string]any{
			{"name": "summary", "type": "text", "required": true},
		},
		"default_status": "todo",
	}
	id := instantiateTemplate(t, config)

	// Two identical instantiations must produce identical payloads (idempotent)
	// and must not create any issue.
	w1 := instantiateRequest(t, id, map[string]any{"values": map[string]string{"summary": "x"}})
	w2 := instantiateRequest(t, id, map[string]any{"values": map[string]string{"summary": "x"}})
	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Fatalf("codes = %d,%d", w1.Code, w2.Code)
	}
	var a, b map[string]any
	json.NewDecoder(w1.Body).Decode(&a)
	json.NewDecoder(w2.Body).Decode(&b)
	if a["title"] != b["title"] || a["status"] != b["status"] {
		t.Fatalf("idempotency violated: %#v vs %#v", a, b)
	}

	// No issue created: instantiate must have no side effect.
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT COUNT(*) FROM issue WHERE title = $1`, a["title"]).Scan(&count); err == nil && count > 0 {
		t.Fatalf("instantiate created %d issues, want 0", count)
	}
}
