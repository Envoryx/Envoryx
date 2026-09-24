-- CPU, memory and process limits per project as JSON (store.ResourceLimits); '' = none.
ALTER TABLE projects ADD COLUMN limits TEXT NOT NULL DEFAULT '';
