-- Reverse RIC-806 agent health state.
ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_health_state_check;
ALTER TABLE agent DROP COLUMN IF EXISTS health_metadata;
ALTER TABLE agent DROP COLUMN IF EXISTS health_state;
