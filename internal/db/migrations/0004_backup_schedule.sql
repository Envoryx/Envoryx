-- Scheduled backups per project.
ALTER TABLE projects ADD COLUMN backup_schedule TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN backup_hour INTEGER NOT NULL DEFAULT 3;
ALTER TABLE projects ADD COLUMN backup_weekday INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN backup_keep INTEGER NOT NULL DEFAULT 7;
ALTER TABLE projects ADD COLUMN backup_include_deps INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN backup_last_run TEXT NOT NULL DEFAULT '';
