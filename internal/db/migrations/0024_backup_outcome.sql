-- The outcome of the scheduled backups: how many runs failed in a row and the message of
-- the latest failure, so a schedule that keeps failing shows as such.
ALTER TABLE projects ADD COLUMN backup_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN backup_last_error TEXT NOT NULL DEFAULT '';
