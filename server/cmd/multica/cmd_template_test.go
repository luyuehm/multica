package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const testTemplateUUID = "22222222-2222-2222-2222-222222222222"

func newTemplateListTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().String("output", "table", "")
	cmd.Flags().Bool("full-id", false, "")
	return cmd
}

func newTemplateGetTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "get"}
	cmd.Flags().String("output", "json", "")
	return cmd
}

func newTemplateCreateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("issue-title", "", "")
	cmd.Flags().String("issue-content", "", "")
	cmd.Flags().String("config", "", "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func newTemplateUpdateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("issue-title", "", "")
	cmd.Flags().String("issue-content", "", "")
	cmd.Flags().String("config", "", "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func newTemplateDeleteTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "delete"}
	cmd.Flags().String("output", "json", "")
	return cmd
}

func templateSummary(name string) map[string]any {
	return map[string]any{
		"id":           testTemplateUUID,
		"workspace_id": "ws-1",
		"name":         name,
		"issue_title":  "[Bug] {{summary}}",
		"config":       map[string]any{},
		"created_by":   nil,
		"created_at":   "2026-09-23T00:00:00Z",
		"updated_at":   "2026-09-23T00:00:00Z",
	}
}

func TestRunTemplateListSendsWorkspaceAndPrintsTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/issue-templates" {
			t.Fatalf("path = %q, want /api/issue-templates", r.URL.Path)
		}
		if r.URL.Query().Get("workspace_id") != "ws-1" {
			t.Fatalf("workspace_id = %q, want ws-1", r.URL.Query().Get("workspace_id"))
		}
		if r.Header.Get("X-Workspace-ID") != "ws-1" {
			t.Fatalf("X-Workspace-ID = %q, want ws-1", r.Header.Get("X-Workspace-ID"))
		}
		_ = json.NewEncoder(w).Encode([]any{templateSummary("Bug")})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newTemplateListTestCmd()
	out, err := captureStdout(t, func() error { return runTemplateList(cmd, nil) })
	if err != nil {
		t.Fatalf("runTemplateList: %v", err)
	}
	if !strings.Contains(out, "Bug") || !strings.Contains(out, "[Bug] {{summary}}") {
		t.Fatalf("stdout = %q, want table with name and title", out)
	}
}

func TestRunTemplateListJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{templateSummary("Bug")})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newTemplateListTestCmd()
	_ = cmd.Flags().Set("output", "json")
	out, err := captureStdout(t, func() error { return runTemplateList(cmd, nil) })
	if err != nil {
		t.Fatalf("runTemplateList: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode stdout JSON: %v\n%s", err, out)
	}
	if len(got) != 1 || got[0]["name"] != "Bug" {
		t.Fatalf("stdout = %#v, want one Bug template", got)
	}
}

func TestRunTemplateCreateSendsExpectedRequest(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/issue-templates" {
			t.Fatalf("path = %q, want /api/issue-templates", r.URL.Path)
		}
		if r.Header.Get("X-Workspace-ID") != "ws-1" {
			t.Fatalf("X-Workspace-ID = %q, want ws-1", r.Header.Get("X-Workspace-ID"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":            testTemplateUUID,
			"workspace_id":  "ws-1",
			"name":          body["name"],
			"issue_title":   body["issue_title"],
			"issue_content": body["issue_content"],
			"config":        body["config"],
			"created_at":    "2026-09-23T00:00:00Z",
			"updated_at":    "2026-09-23T00:00:00Z",
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newTemplateCreateTestCmd()
	_ = cmd.Flags().Set("name", "Bug Report")
	_ = cmd.Flags().Set("issue-title", "[Bug] {{summary}}")
	_ = cmd.Flags().Set("issue-content", "Describe the bug here.")
	_ = cmd.Flags().Set("config", `{"variables":[{"name":"summary","type":"text","required":true}]}`)

	out, err := captureStdout(t, func() error { return runTemplateCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("runTemplateCreate: %v", err)
	}
	if body["name"] != "Bug Report" || body["issue_title"] != "[Bug] {{summary}}" {
		t.Fatalf("body = %#v, want name/issue_title", body)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode stdout JSON: %v\n%s", err, out)
	}
	if got["id"] != testTemplateUUID || got["name"] != "Bug Report" {
		t.Fatalf("stdout = %#v, want created template", got)
	}
}

func TestRunTemplateCreateRequiresNameAndTitle(t *testing.T) {
	cmd := newTemplateCreateTestCmd()
	if err := runTemplateCreate(cmd, nil); err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Fatalf("runTemplateCreate error = %v, want missing name", err)
	}
	_ = cmd.Flags().Set("name", "Bug Report")
	if err := runTemplateCreate(cmd, nil); err == nil || !strings.Contains(err.Error(), "--issue-title is required") {
		t.Fatalf("runTemplateCreate error = %v, want missing title", err)
	}
}

func TestRunTemplateCreateRejectsNonObjectConfig(t *testing.T) {
	cmd := newTemplateCreateTestCmd()
	_ = cmd.Flags().Set("name", "Bug Report")
	_ = cmd.Flags().Set("issue-title", "[Bug] {{summary}}")
	_ = cmd.Flags().Set("config", `[1,2,3]`)
	if err := runTemplateCreate(cmd, nil); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("runTemplateCreate error = %v, want non-object config rejected", err)
	}
}

func TestRunTemplateUpdateSendsOnlyChangedFields(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/api/issue-templates/"+testTemplateUUID {
			t.Fatalf("path = %q, want template path", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":           testTemplateUUID,
			"workspace_id": "ws-1",
			"name":         body["name"],
			"issue_title":  "[Bug] {{summary}}",
			"config":       map[string]any{},
			"created_at":   "2026-09-23T00:00:00Z",
			"updated_at":   "2026-09-23T00:00:00Z",
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newTemplateUpdateTestCmd()
	_ = cmd.Flags().Set("name", "Bug Report v2")

	if _, err := captureStdout(t, func() error { return runTemplateUpdate(cmd, []string{testTemplateUUID}) }); err != nil {
		t.Fatalf("runTemplateUpdate: %v", err)
	}
	if len(body) != 1 || body["name"] != "Bug Report v2" {
		t.Fatalf("body = %#v, want only name field", body)
	}
}

func TestRunTemplateUpdateNothingToUpdate(t *testing.T) {
	cmd := newTemplateUpdateTestCmd()
	if err := runTemplateUpdate(cmd, []string{testTemplateUUID}); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("runTemplateUpdate error = %v, want nothing-to-update", err)
	}
}

func TestRunTemplateDeletePrintsJsonConfirmation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/api/issue-templates/"+testTemplateUUID {
			t.Fatalf("path = %q, want template path", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newTemplateDeleteTestCmd()
	_ = cmd.Flags().Set("output", "json")

	out, err := captureStdout(t, func() error { return runTemplateDelete(cmd, []string{testTemplateUUID}) })
	if err != nil {
		t.Fatalf("runTemplateDelete: %v", err)
	}
	if !strings.Contains(out, `"deleted": true`) || !strings.Contains(out, testTemplateUUID) {
		t.Fatalf("stdout = %q, want deleted JSON confirmation", out)
	}
}

func TestResolveTemplateRefByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/issue-templates" {
			_ = json.NewEncoder(w).Encode([]any{templateSummary("Bug Report")})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	client, err := newAPIClient(&cobra.Command{})
	if err != nil {
		t.Fatalf("newAPIClient: %v", err)
	}
	ctx := t.Context()
	ref, err := resolveTemplateRef(ctx, client, "bug report")
	if err != nil {
		t.Fatalf("resolveTemplateRef: %v", err)
	}
	if ref.ID != testTemplateUUID || ref.Display != "Bug Report" {
		t.Fatalf("ref = %#v, want id %s / display Bug Report", ref, testTemplateUUID)
	}
}

func TestResolveTemplateRefByPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/issue-templates" {
			_ = json.NewEncoder(w).Encode([]any{templateSummary("Bug Report")})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	client, err := newAPIClient(&cobra.Command{})
	if err != nil {
		t.Fatalf("newAPIClient: %v", err)
	}
	ref, err := resolveTemplateRef(t.Context(), client, "22222222")
	if err != nil {
		t.Fatalf("resolveTemplateRef: %v", err)
	}
	if ref.ID != testTemplateUUID {
		t.Fatalf("ref = %#v, want id %s", ref, testTemplateUUID)
	}
}

func TestResolveTemplateRefUnknownName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/issue-templates" {
			_ = json.NewEncoder(w).Encode([]any{templateSummary("Bug Report")})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	client, err := newAPIClient(&cobra.Command{})
	if err != nil {
		t.Fatalf("newAPIClient: %v", err)
	}
	_, err = resolveTemplateRef(t.Context(), client, "nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resolveTemplateRef error = %v, want not found", err)
	}
}
