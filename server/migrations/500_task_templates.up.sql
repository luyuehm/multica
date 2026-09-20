-- Workspace-level Task template definitions (MUL-* / RIC-902).
--
-- Sibling model to issue_property (migration 191): a workspace-scoped catalog
-- of named templates used to prefill an issue when it is created. No foreign
-- keys by repository rule — workspace membership and actor references are
-- validated in application code.
--
-- Minimal field set for the MVP migration:
--   - template text (title_template / description_template) is nullable: a
--     template may supply only defaults, leaving the prompt open.
--   - variables holds the variable-descriptor set for template interpolation,
--     kept as an object so the shape can evolve without another migration.
--   - enabled + position drive ordering in the issue-create picker; position
--     is FLOAT to match issue_property's drag ordering.
--   - created_by_id / updated_by_id are actor ids (member/agent); no FK per
--     repository rule.
--
-- The workspace_id + name uniqueness lives in a follow-up single-statement
-- migration (501) because CREATE UNIQUE INDEX CONCURRENTLY cannot share a
-- migration with other statements.

CREATE TABLE task_templates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    title_template TEXT,
    description_template TEXT,
    variables JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(variables) = 'object'),
    enabled BOOLEAN NOT NULL DEFAULT true,
    position FLOAT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by_id UUID,
    updated_by_id UUID
);