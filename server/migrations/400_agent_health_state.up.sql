-- Agent machine-readable health / routing state (RIC-806).
--
-- Prior to this migration, an agent could only be retired via `archived_at`
-- (soft delete) or have its runtime marked offline via `agent_runtime.status`.
-- A model that was described as "QUARANTINED" in its description text remained
-- active and bindable: nothing in the routing path parsed that description, so
-- nothing stopped an assignment or autopilot from dispatching work to it.
--
-- `health_state` is the machine-readable gate the router actually filters on.
-- Values:
--   active       — eligible for new work (default).
--   standby      — visible but held out of automatic assignment; a human may
--                  still dispatch explicitly. Kept distinct from `archived_at`
--                  so an agent's history and configuration stay intact without
--                  the "deleted" semantics of archive.
--   quarantined  — isolated after probe failure or manual action. NOT eligible
--                  for automatic or human assignment until a recovery gate
--                  promotes it back to active/standby.
--   disabled     — hard-stopped. Never eligible for assignment. Use for models
--                  that have been withdrawn (e.g. gpt-5.3-codex) where archive
--                  would lose the row the platform still needs to reason about.
--
-- `health_metadata` is the structured probe / audit registry. It stores the
-- last probe result (last_probe/status/error/latency), the recovery counter
-- (consecutive_successes), the required threshold, and the auditor identity
-- for any manual override. NULL means "no probe has run and no override
-- recorded"; the router treats NULL identically to `active`.
--
-- The column is nullable-text with a CHECK constraint (not an enum) so the
-- state is readable in any DB tool and the value set is enforced server-side
-- without a migration-time enum dependency. `active` is the default so every
-- existing agent — including the 25 in the workspace — is eligible by default
-- and only the explicit few the operator names move to quarantined/disabled.
ALTER TABLE agent ADD COLUMN health_state TEXT;
ALTER TABLE agent ADD COLUMN health_metadata JSONB;

-- Backfill the default for existing rows so NULL-vs-active is unambiguous:
-- after this, NULL can ONLY mean "column untouched since creation", which the
-- router treats as active. Rows that were never probed stay active.
UPDATE agent SET health_state = 'active' WHERE health_state IS NULL;

-- Enforce the value set. Added after the backfill so the constraint sees
-- only valid values. IF NOT EXISTS guards a re-run after a partial rollback.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'agent_health_state_check'
    ) THEN
        ALTER TABLE agent ADD CONSTRAINT agent_health_state_check
            CHECK (health_state IS NULL OR health_state IN ('active', 'standby', 'quarantined', 'disabled'));
    END IF;
END $$;

-- Default for new rows. Applied last so the backfill above is the source of
-- truth for existing rows; setting the default before the backfill would have
-- left pre-existing rows NULL until the UPDATE ran.
ALTER TABLE agent ALTER COLUMN health_state SET DEFAULT 'active';
