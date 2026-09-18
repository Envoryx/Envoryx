-- Per-project opt-in for JetBrains Gateway (IDE backend inside the container).
ALTER TABLE projects ADD COLUMN ide_gateway INTEGER NOT NULL DEFAULT 0;
