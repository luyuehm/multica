-- Case-insensitive template-name uniqueness within a workspace without
-- blocking writes (same pattern as idx_issue_property_ws_name, migration 194).
-- Keep this as the migration's only statement: PostgreSQL rejects CREATE INDEX
-- CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_task_templates_ws_name
    ON task_templates (workspace_id, LOWER(name));