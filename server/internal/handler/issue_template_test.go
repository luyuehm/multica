package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIssueTemplateCRUD(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	createReq := map[string]any{
		"name":          "Bug report",
		"issue_title":   "Investigate {{area}} bug",
		"issue_content": "## Context\n\n## Steps\n",
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
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created response missing id: %#v", created)
	}
	if created["name"] != "Bug report" || created["issue_title"] != "Investigate {{area}} bug" {
		t.Fatalf("created response mismatch: %#v", created)
	}
	t.Cleanup(func() {
		req := withURLParam(newRequest(http.MethodDelete, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, nil), "id", id)
		testHandler.DeleteIssueTemplate(httptest.NewRecorder(), req)
	})

	w = httptest.NewRecorder()
	testHandler.ListIssueTemplates(w, newRequest(http.MethodGet, "/api/issue-templates?workspace_id="+testWorkspaceID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssueTemplates status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodGet, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, nil), "id", id)
	testHandler.GetIssueTemplate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssueTemplate status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = withURLParam(newRequest(http.MethodPut, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, map[string]any{
		"name":          "Bug triage",
		"issue_title":   "Triage bug",
		"issue_content": "Updated body",
	}), "id", id)
	testHandler.UpdateIssueTemplate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssueTemplate status = %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = withURLParam(newRequest(http.MethodDelete, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, nil), "id", id)
	testHandler.DeleteIssueTemplate(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteIssueTemplate status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestIssueTemplateListOmitsContent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	createReq := map[string]any{
		"name":          "Content test template",
		"issue_title":   "Title here",
		"issue_content": "## Long body that should not appear in list",
	}
	w := httptest.NewRecorder()
	testHandler.CreateIssueTemplate(w, newRequest(http.MethodPost, "/api/issue-templates?workspace_id="+testWorkspaceID, createReq))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssueTemplate status = %d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.NewDecoder(w.Body).Decode(&created)
	id := created["id"].(string)
	t.Cleanup(func() {
		req := withURLParam(newRequest(http.MethodDelete, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, nil), "id", id)
		testHandler.DeleteIssueTemplate(httptest.NewRecorder(), req)
	})

	w = httptest.NewRecorder()
	testHandler.ListIssueTemplates(w, newRequest(http.MethodGet, "/api/issue-templates?workspace_id="+testWorkspaceID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssueTemplates status = %d", w.Code)
	}
	var list []map[string]any
	json.NewDecoder(w.Body).Decode(&list)
	for _, item := range list {
		if _, hasContent := item["issue_content"]; hasContent {
			t.Fatalf("list response should not contain issue_content, got: %v", item)
		}
	}

	w = httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodGet, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, nil), "id", id)
	testHandler.GetIssueTemplate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssueTemplate status = %d", w.Code)
	}
	var detail map[string]any
	json.NewDecoder(w.Body).Decode(&detail)
	if detail["issue_content"] != "## Long body that should not appear in list" {
		t.Fatalf("detail should contain issue_content, got: %v", detail["issue_content"])
	}
}

func TestIssueTemplateArchiveUnarchive(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	createReq := map[string]any{
		"name":        "Archive round trip",
		"issue_title": "Investigate {{area}} bug",
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
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created response missing id: %#v", created)
	}
	t.Cleanup(func() {
		req := withURLParam(newRequest(http.MethodDelete, "/api/issue-templates/"+id+"?workspace_id="+testWorkspaceID, nil), "id", id)
		testHandler.DeleteIssueTemplate(httptest.NewRecorder(), req)
	})

	w = httptest.NewRecorder()
	archiveReq := withURLParam(newRequest(http.MethodPost, "/api/issue-templates/"+id+"/archive?workspace_id="+testWorkspaceID, nil), "id", id)
	testHandler.ArchiveIssueTemplate(w, archiveReq)
	if w.Code != http.StatusOK {
		t.Fatalf("ArchiveIssueTemplate status = %d body=%s", w.Code, w.Body.String())
	}
	var archived map[string]any
	if err := json.NewDecoder(w.Body).Decode(&archived); err != nil {
		t.Fatal(err)
	}
	if archived["archived_at"] == nil || archived["archived_at"] == "" {
		t.Fatalf("archived response missing archived_at: %#v", archived)
	}

	w = httptest.NewRecorder()
	testHandler.ListIssueTemplates(w, newRequest(http.MethodGet, "/api/issue-templates?workspace_id="+testWorkspaceID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssueTemplates(status without archived) = %d body=%s", w.Code, w.Body.String())
	}
	var activeList []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&activeList); err != nil {
		t.Fatal(err)
	}
	if containsTemplateID(activeList, id) {
		t.Fatalf("active list should hide archived template, got %#v", activeList)
	}

	w = httptest.NewRecorder()
	testHandler.ListIssueTemplates(w, newRequest(http.MethodGet, "/api/issue-templates?workspace_id="+testWorkspaceID+"&include_archived=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssueTemplates(include archived) status = %d body=%s", w.Code, w.Body.String())
	}
	var archivedList []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&archivedList); err != nil {
		t.Fatal(err)
	}
	if !containsTemplateID(archivedList, id) {
		t.Fatalf("include_archived list should include template, got %#v", archivedList)
	}

	w = httptest.NewRecorder()
	unarchiveReq := withURLParam(newRequest(http.MethodPost, "/api/issue-templates/"+id+"/unarchive?workspace_id="+testWorkspaceID, nil), "id", id)
	testHandler.UnarchiveIssueTemplate(w, unarchiveReq)
	if w.Code != http.StatusOK {
		t.Fatalf("UnarchiveIssueTemplate status = %d body=%s", w.Code, w.Body.String())
	}
	var restored map[string]any
	if err := json.NewDecoder(w.Body).Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if restored["archived_at"] != nil {
		t.Fatalf("restored response should omit archived_at, got %#v", restored)
	}
}

func containsTemplateID(items []map[string]any, id string) bool {
	for _, item := range items {
		if item["id"] == id {
			return true
		}
	}
	return false
}

func TestIssueTemplateValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing name",
			body: map[string]any{
				"issue_title":   "Title",
				"issue_content": "Content",
			},
		},
		{
			name: "missing issue title",
			body: map[string]any{
				"name":          "Template",
				"issue_content": "Content",
			},
		},
		{
			name: "config is array",
			body: map[string]any{
				"name":        "Template",
				"issue_title": "Title",
				"config":      []any{"a", "b"},
			},
		},
		{
			name: "config is scalar",
			body: map[string]any{
				"name":        "Template",
				"issue_title": "Title",
				"config":      "invalid",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.CreateIssueTemplate(w, newRequest(http.MethodPost, "/api/issue-templates?workspace_id="+testWorkspaceID, tt.body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("CreateIssueTemplate status = %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
