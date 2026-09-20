-- Rollback for 514: table drop must precede the 515 index drop in reverse
-- order; both are IF EXISTS so this stays safe if the concurrent index was
-- never created on a database that applied 514 alone.
DROP TABLE IF EXISTS task_templates;