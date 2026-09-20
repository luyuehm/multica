-- Rollback for 533: table drop must precede the 534 index drop in reverse
-- order; both are IF EXISTS so this stays safe if the concurrent index was
-- never created on a database that applied 533 alone.
DROP TABLE IF EXISTS task_templates;