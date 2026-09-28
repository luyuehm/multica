package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/tasktemplate"
	"github.com/multica-ai/multica/server/internal/util"
)

// instantiateConfig is the Task-template instantiate extension stored inside
// the issue_template.config JSONB. It is additive: templates without it
// instantiate to a plain copy of issue_title/issue_content (the pre-upstream
// apply behavior), templates with it opt into variable interpolation and
// default Issue field prefill.
//
// Field names match the RIC-902 task_templates data-model audit so nothing
// changes if that table is later materialized.
type instantiateConfig struct {
	VariableDecls []tasktemplate.Variable `json:"variables"`

	DefaultPriority      *string `json:"default_priority"`
	DefaultStatus        *string `json:"default_status"`
	DefaultAssigneeType  *string `json:"default_assignee_type"`
	DefaultAssigneeID    *string `json:"default_assignee_id"`
	DefaultProjectID     *string `json:"default_project_id"`
	DefaultStage         *int32  `json:"default_stage"`
	DefaultStartDateOffsetDays *int32 `json:"default_start_date_offset_days"`
	DefaultDueDateOffsetDays   *int32 `json:"default_due_date_offset_days"`

	DefaultPropertyValues map[string]json.RawMessage `json:"default_property_values"`
	DefaultMetadata       map[string]any            `json:"default_metadata"`
	DefaultLabels         []string                  `json:"default_labels"`
}

// parseInstantiateConfig decodes the config JSONB into the instantiate
// extension. Unknown top-level keys are tolerated (the config is shared with
// other consumers); a malformed variables block is a hard error so a template
// that declares variables always gets them validated at edit time, never
// silently at instantiate time.
func parseInstantiateConfig(raw []byte) (instantiateConfig, error) {
	var cfg instantiateConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid template config: %w", err)
	}
	if err := tasktemplate.ValidateVariables(cfg.VariableDecls); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// InstantiateIssueTemplateRequest carries the caller-supplied variable
// values. Values for undeclared variables are rejected rather than ignored.
type InstantiateIssueTemplateRequest struct {
	Values map[string]string `json:"values"`
}

// InstantiateIssueTemplateResponse is the editable Issue prefill payload. It
// mirrors the shape the issue-create flow accepts (title, description, and the
// default fields) so the response can be handed back to POST /api/issues as-is.
type InstantiateIssueTemplateResponse struct {
	Title        string                    `json:"title"`
	Description  string                    `json:"description,omitempty"`
	Variables    []tasktemplate.Variable   `json:"variables,omitempty"`
	Status       *string                   `json:"status,omitempty"`
	Priority     *string                   `json:"priority,omitempty"`
	AssigneeType *string                   `json:"assignee_type,omitempty"`
	AssigneeID   *string                   `json:"assignee_id,omitempty"`
	ProjectID    *string                   `json:"project_id,omitempty"`
	Stage        *int32                    `json:"stage,omitempty"`
	StartDate    *string                   `json:"start_date,omitempty"`
	DueDate      *string                   `json:"due_date,omitempty"`
	Properties   map[string]any            `json:"properties,omitempty"`
	Metadata     map[string]any            `json:"metadata,omitempty"`
	LabelIDs     []string                  `json:"label_ids,omitempty"`
}

// InstantiateIssueTemplate resolves a Task template into an editable Issue
// prefill payload. It is read-only and idempotent: it performs no writes and
// creates no Issue, so calling it repeatedly with the same values yields the
// same payload. Interpolated {{variable}} tokens are rejected whenever they
// reference an undeclared or unresolved variable, and every configured default
// (assignee, project, label, property, status, priority) is validated against
// the workspace now, not at template-edit time.
func (h *Handler) InstantiateIssueTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	template, ok := h.loadIssueTemplateForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req InstantiateIssueTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Values == nil {
		req.Values = map[string]string{}
	}

	cfg, err := parseInstantiateConfig(template.Config)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_template_variables", err.Error())
		return
	}

	// Resolve values (defaults + type checks + redundancy/unknown checks) —
	// a missing required variable, an unknown value name, or a type mismatch
	// fails the whole instantiate (RIC-903 acceptance: 缺失/冗余/非法变量失败).
	resolved, err := tasktemplate.ResolveVariables(cfg.VariableDecls, req.Values)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_template_variables", err.Error())
		return
	}

	// Interpolate title and content; unresolved/undeclared tokens fail.
	title, err := tasktemplate.Interpolate(template.IssueTitle, cfg.VariableDecls, resolved)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_template_variables", err.Error())
		return
	}
	desc, err := tasktemplate.Interpolate(template.IssueContent, cfg.VariableDecls, resolved)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_template_variables", err.Error())
		return
	}

	resp := InstantiateIssueTemplateResponse{
		Title:       title,
		Description: desc,
		Variables:   cfg.VariableDecls,
	}

	// Default status: resolved through the workspace status catalog exactly as
	// issue-create does, so a status that was archived after the template was
	// configured fails here rather than at issue-create.
	if cfg.DefaultStatus != nil && *cfg.DefaultStatus != "" {
		status, ok := h.resolveIssueStatusKey(w, r, workspaceUUID, *cfg.DefaultStatus)
		if !ok {
			return
		}
		statusVal := status
		resp.Status = &statusVal
	}

	// Default priority.
	if cfg.DefaultPriority != nil && *cfg.DefaultPriority != "" {
		if !validateIssueEnum(w, "priority", *cfg.DefaultPriority, validIssuePriorities) {
			return
		}
		priority := *cfg.DefaultPriority
		resp.Priority = &priority
	}

	// Default assignee: real-time pair validation (member/agent/squad, archived
	// guards, invocation permission) shared with issue-create.
	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if cfg.DefaultAssigneeType != nil {
		assigneeType = pgtype.Text{String: *cfg.DefaultAssigneeType, Valid: true}
	}
	if cfg.DefaultAssigneeID != nil {
		id, ok := parseUUIDOrBadRequest(w, *cfg.DefaultAssigneeID, "default_assignee_id")
		if !ok {
			return
		}
		assigneeID = id
	}
	if status, msg := h.validateAssigneePair(r.Context(), r, workspaceID, assigneeType, assigneeID); status != 0 {
		writeError(w, status, msg)
		return
	}
	if cfg.DefaultAssigneeType != nil && *cfg.DefaultAssigneeType != "" {
		t, id := *cfg.DefaultAssigneeType, uuidToString(assigneeID)
		resp.AssigneeType = &t
		resp.AssigneeID = &id
	}

	// Default project: must exist in this workspace.
	if cfg.DefaultProjectID != nil && *cfg.DefaultProjectID != "" {
		projectID, ok := parseUUIDOrBadRequest(w, *cfg.DefaultProjectID, "default_project_id")
		if !ok {
			return
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID:          projectID,
			WorkspaceID: workspaceUUID,
		}); err != nil {
			writeErrorCode(w, http.StatusBadRequest, "invalid_template_default", "default project not found in this workspace")
			return
		}
		pid := uuidToString(projectID)
		resp.ProjectID = &pid
	}

	// Default labels: every id must be an issue label in this workspace.
	if len(cfg.DefaultLabels) > 0 {
		labelIDs := make([]string, 0, len(cfg.DefaultLabels))
		for _, rawID := range cfg.DefaultLabels {
			id, ok := parseUUIDOrBadRequest(w, rawID, "default_label_id")
			if !ok {
				return
			}
			label, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{
				ID:          id,
				WorkspaceID: workspaceUUID,
			})
			if err != nil {
				writeErrorCode(w, http.StatusBadRequest, "invalid_template_default", "default label not found in this workspace")
				return
			}
			if label.ResourceType != "issue" {
				writeErrorCode(w, http.StatusBadRequest, "invalid_template_default", "default label is not an issue label")
				return
			}
			labelIDs = append(labelIDs, uuidToString(id))
		}
		resp.LabelIDs = labelIDs
	}

	// Default properties: each value is type-checked against the current
	// property definition, not a template-edit-time snapshot.
	if len(cfg.DefaultPropertyValues) > 0 {
		if _, ok := parseIssueCreateProperties(w, cfg.DefaultPropertyValues); !ok {
			return
		}
		props := make(map[string]any, len(cfg.DefaultPropertyValues))
		for rawPropertyID, rawValue := range cfg.DefaultPropertyValues {
			propertyID, err := util.ParseUUID(rawPropertyID)
			if err != nil {
				writeErrorCode(w, http.StatusBadRequest, "invalid_issue_property", "default property id must be a UUID")
				return
			}
			def, err := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{
				ID:          propertyID,
				WorkspaceID: workspaceUUID,
			})
			if err != nil {
				writeErrorCode(w, http.StatusBadRequest, "invalid_template_default", "default property definition not found in this workspace")
				return
			}
			validated, err := validatePropertyValue(def, rawValue)
			if err != nil {
				writeIssueCreatePropertyError(w, err)
				return
			}
			var val any
			if err := json.Unmarshal(validated, &val); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to encode default property value")
				return
			}
			props[uuidToString(propertyID)] = val
		}
		resp.Properties = props
	}

	// Default metadata: key/value rules shared with the issue metadata endpoint.
	if len(cfg.DefaultMetadata) > 0 {
		meta := make(map[string]any, len(cfg.DefaultMetadata))
		for key, value := range cfg.DefaultMetadata {
			if err := validateIssueMetadataKey(key); err != nil {
				writeErrorCode(w, http.StatusBadRequest, "invalid_issue_metadata", err.Error())
				return
			}
			rawValue, err := json.Marshal(value)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to encode default metadata value")
				return
			}
			if err := validateIssueMetadataValue(rawValue); err != nil {
				writeErrorCode(w, http.StatusBadRequest, "invalid_issue_metadata", err.Error())
				return
			}
			meta[key] = value
		}
		if len(meta) > maxIssueMetadataKeys {
			writeErrorCode(w, http.StatusBadRequest, "invalid_issue_metadata", fmt.Sprintf("metadata exceeds the %d-key limit", maxIssueMetadataKeys))
			return
		}
		resp.Metadata = meta
	}

	// Date offsets: computed from "today" so the prefill payload carries a
	// concrete start/due date while the template stores only a stable offset.
	today := time.Now().UTC()
	if cfg.DefaultStartDateOffsetDays != nil {
		start := today.AddDate(0, 0, int(*cfg.DefaultStartDateOffsetDays)).Format("2006-01-02")
		resp.StartDate = &start
	}
	if cfg.DefaultDueDateOffsetDays != nil {
		due := today.AddDate(0, 0, int(*cfg.DefaultDueDateOffsetDays)).Format("2006-01-02")
		resp.DueDate = &due
	}

	// Default stage.
	if cfg.DefaultStage != nil {
		if *cfg.DefaultStage < 1 {
			writeError(w, http.StatusBadRequest, "default stage must be >= 1")
			return
		}
		stage := *cfg.DefaultStage
		resp.Stage = &stage
	}

	writeJSON(w, http.StatusOK, resp)
}