-- RIC-947: allow an autopilot to define a standard body template applied when
-- create_issue dispatch builds the issue description. NULL keeps the legacy
-- behavior (description copied verbatim + system footer), so existing
-- autopilots are unaffected.
ALTER TABLE autopilot ADD COLUMN IF NOT EXISTS issue_body_template TEXT;