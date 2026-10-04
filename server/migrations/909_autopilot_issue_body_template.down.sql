-- RIC-947: revert the autopilot issue body template column. idempotent IF EXISTS
-- so a partial apply can be rolled back cleanly.
ALTER TABLE autopilot DROP COLUMN IF EXISTS issue_body_template;