package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Template commands — workspace-scoped CRUD for issue (task) templates.
//
// The upstream fork merged the Task-template MVP as "issue templates": a
// workspace-scoped record of a reusable issue title/content with an opaque
// JSON config (variables and defaults live in config for the instantiate
// API). This command exposes that surface as `multica template`. There is no
// archive/unarchive in the merged API — removal is a hard DELETE.
// ---------------------------------------------------------------------------

type templateSummaryDTO struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	IssueTitle  string `json:"issue_title"`
	Config      any    `json:"config"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type templateDTO struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	Name         string `json:"name"`
	IssueTitle   string `json:"issue_title"`
	IssueContent string `json:"issue_content"`
	Config       any    `json:"config"`
	CreatedBy    *string `json:"created_by"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage workspace issue templates",
	Long: `Manage workspace issue templates.

Issue templates are reusable issue drafts: a name, a title template, an
optional content template, and an opaque JSON config. The config carries the
variable declarations and default-field overrides used by the template
instantiate API (issue create --template is not implemented).

Templates are addressed BY NAME (case-insensitive) or by id prefix; the CLI
resolves them to the UUIDs the API expects.`,
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List issue templates in the workspace",
	Args:  exactArgs(0),
	RunE:  runTemplateList,
}

var templateGetCmd = &cobra.Command{
	Use:   "get <id-or-name>",
	Short: "Show one issue template",
	Args:  exactArgs(1),
	RunE:  runTemplateGet,
}

var templateCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an issue template",
	Args:  exactArgs(0),
	RunE:  runTemplateCreate,
}

var templateUpdateCmd = &cobra.Command{
	Use:   "update <id-or-name>",
	Short: "Update an issue template",
	Args:  exactArgs(1),
	RunE:  runTemplateUpdate,
}

var templateDeleteCmd = &cobra.Command{
	Use:   "delete <id-or-name>",
	Short: "Delete an issue template",
	Args:  exactArgs(1),
	RunE:  runTemplateDelete,
}

func init() {
	templateCmd.AddCommand(templateListCmd)
	templateCmd.AddCommand(templateGetCmd)
	templateCmd.AddCommand(templateCreateCmd)
	templateCmd.AddCommand(templateUpdateCmd)
	templateCmd.AddCommand(templateDeleteCmd)

	templateListCmd.Flags().String("output", "table", "Output format: table or json")
	templateListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	templateGetCmd.Flags().String("output", "json", "Output format: table or json")

	templateCreateCmd.Flags().String("name", "", "Template name (required)")
	templateCreateCmd.Flags().String("issue-title", "", "Issue title template (required)")
	templateCreateCmd.Flags().String("issue-content", "", "Issue content template")
	templateCreateCmd.Flags().String("config", "", "JSON object with template config (variables, defaults)")
	templateCreateCmd.Flags().String("output", "json", "Output format: table or json")

	templateUpdateCmd.Flags().String("name", "", "New template name")
	templateUpdateCmd.Flags().String("issue-title", "", "New issue title template")
	templateUpdateCmd.Flags().String("issue-content", "", "New issue content template")
	templateUpdateCmd.Flags().String("config", "", "New JSON config object")
	templateUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	templateDeleteCmd.Flags().String("output", "json", "Output format: table or json")
}

func fetchTemplateSummaries(ctx context.Context, client *cli.APIClient) ([]templateSummaryDTO, error) {
	if client.WorkspaceID == "" {
		return nil, fmt.Errorf("workspace_id is required to list issue templates")
	}
	params := url.Values{"workspace_id": {client.WorkspaceID}}
	var summaries []templateSummaryDTO
	if err := client.GetJSON(ctx, "/api/issue-templates?"+params.Encode(), &summaries); err != nil {
		return nil, fmt.Errorf("list issue templates: %w", err)
	}
	return summaries, nil
}

// resolveTemplateRef matches a CLI ref against the workspace templates by
// UUID first (full UUID or unambiguous id prefix), then case-insensitive name.
func resolveTemplateRef(ctx context.Context, client *cli.APIClient, ref string) (resolvedID, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return resolvedID{}, fmt.Errorf("template id or name is required")
	}
	if uuidRegexp.MatchString(trimmed) {
		return resolvedID{ID: trimmed, Display: trimmed}, nil
	}

	summaries, err := fetchTemplateSummaries(ctx, client)
	if err != nil {
		return resolvedID{}, err
	}

	// Id prefix match first (same addressing contract as labels).
	prefix, prefixErr := normalizeUUIDPrefix(trimmed)
	if prefixErr == nil {
		matches := make([]idCandidate, 0, 1)
		for _, s := range summaries {
			if s.ID == "" {
				continue
			}
			if strings.HasPrefix(compactUUID(s.ID), prefix) {
				matches = append(matches, idCandidate{ID: s.ID, Display: s.Name})
			}
		}
		switch len(matches) {
		case 1:
			return resolvedID{ID: matches[0].ID, Display: matches[0].Display}, nil
		case 0:
			// fall through to name match
		default:
			return resolvedID{}, ambiguousIDPrefixError("template", trimmed, matches)
		}
	}

	// Case-insensitive name match.
	lower := strings.ToLower(trimmed)
	names := make([]string, len(summaries))
	for _, s := range summaries {
		if strings.ToLower(s.Name) == lower {
			display := s.Name
			return resolvedID{ID: s.ID, Display: display}, nil
		}
		names = append(names, s.Name)
	}
	if len(names) > 0 {
		return resolvedID{}, fmt.Errorf("template %q not found; available: %s", ref, strings.Join(names, ", "))
	}
	return resolvedID{}, fmt.Errorf("template %q not found; run 'multica template list' to see available templates", ref)
}

func runTemplateList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	summaries, err := fetchTemplateSummaries(ctx, client)
	if err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, summaries)
	}

	fullID, _ := cmd.Flags().GetBool("full-id")
	headers := []string{"ID", "NAME", "TITLE", "UPDATED"}
	rows := make([][]string, 0, len(summaries))
	for _, s := range summaries {
		updated := s.UpdatedAt
		if len(updated) >= 10 {
			updated = updated[:10]
		}
		rows = append(rows, []string{
			displayID(s.ID, fullID),
			s.Name,
			s.IssueTitle,
			updated,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runTemplateGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	templateRef, err := resolveTemplateRef(ctx, client, args[0])
	if err != nil {
		return err
	}

	var template templateDTO
	if err := client.GetJSON(ctx, "/api/issue-templates/"+templateRef.ID, &template); err != nil {
		return fmt.Errorf("get issue template: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"ID", "NAME", "TITLE", "UPDATED"}
		updated := template.UpdatedAt
		if len(updated) >= 10 {
			updated = updated[:10]
		}
		rows := [][]string{{
			template.ID,
			template.Name,
			template.IssueTitle,
			updated,
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}
	return cli.PrintJSON(os.Stdout, template)
}

func parseTemplateConfigFlag(flag, raw string) (json.RawMessage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", flag, err)
	}
	if obj == nil {
		return nil, fmt.Errorf("%s must be a JSON object", flag)
	}
	return json.RawMessage(raw), nil
}

func runTemplateCreate(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	issueTitle, _ := cmd.Flags().GetString("issue-title")
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("--name is required")
	}
	if strings.TrimSpace(issueTitle) == "" {
		return fmt.Errorf("--issue-title is required")
	}

	configRaw, err := parseTemplateConfigFlag("--config", func() string {
		v, _ := cmd.Flags().GetString("config")
		return v
	}())
	if err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{
		"name":        strings.TrimSpace(name),
		"issue_title": strings.TrimSpace(issueTitle),
	}
	if v, _ := cmd.Flags().GetString("issue-content"); v != "" {
		body["issue_content"] = v
	}
	if configRaw != nil {
		body["config"] = json.RawMessage(configRaw)
	}

	var template templateDTO
	if err := client.PostJSON(ctx, "/api/issue-templates", body, &template); err != nil {
		return fmt.Errorf("create issue template: %w", err)
	}

	return printTemplateResult(cmd, &template)
}

func runTemplateUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	templateRef, err := resolveTemplateRef(ctx, client, args[0])
	if err != nil {
		return err
	}

	body := map[string]any{}
	if v, _ := cmd.Flags().GetString("name"); v != "" {
		body["name"] = v
	}
	if v, _ := cmd.Flags().GetString("issue-title"); v != "" {
		body["issue_title"] = v
	}
	if v, _ := cmd.Flags().GetString("issue-content"); v != "" {
		body["issue_content"] = v
	}
	if raw, _ := cmd.Flags().GetString("config"); raw != "" {
		configRaw, err := parseTemplateConfigFlag("--config", raw)
		if err != nil {
			return err
		}
		body["config"] = json.RawMessage(configRaw)
	}
	if len(body) == 0 {
		return fmt.Errorf("nothing to update: pass at least one of --name, --issue-title, --issue-content, --config")
	}

	var template templateDTO
	if err := client.PutJSON(ctx, "/api/issue-templates/"+templateRef.ID, body, &template); err != nil {
		return fmt.Errorf("update issue template: %w", err)
	}

	return printTemplateResult(cmd, &template)
}

func runTemplateDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	templateRef, err := resolveTemplateRef(ctx, client, args[0])
	if err != nil {
		return err
	}

	if err := client.DeleteJSON(ctx, "/api/issue-templates/"+templateRef.ID); err != nil {
		return fmt.Errorf("delete issue template: %w", err)
	}
	// JSON consumers get machine-readable output; humans get natural language.
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"id": templateRef.ID, "deleted": true})
	}
	fmt.Fprintf(os.Stdout, "Issue template %s deleted.\n", templateRef.Display)
	return nil
}

func printTemplateResult(cmd *cobra.Command, template *templateDTO) error {
	if output, _ := cmd.Flags().GetString("output"); output == "table" {
		headers := []string{"ID", "NAME", "TITLE", "UPDATED"}
		updated := template.UpdatedAt
		if len(updated) >= 10 {
			updated = updated[:10]
		}
		rows := [][]string{{
			template.ID,
			template.Name,
			template.IssueTitle,
			updated,
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}
	return cli.PrintJSON(os.Stdout, template)
}
