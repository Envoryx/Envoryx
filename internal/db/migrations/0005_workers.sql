-- Long-running worker processes per project (queue workers, schedulers).
CREATE TABLE project_workers (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    preset     TEXT NOT NULL,
    args       TEXT NOT NULL DEFAULT '[]',
    enabled    INTEGER NOT NULL DEFAULT 1,
    position   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    UNIQUE(project_id, name)
);
CREATE INDEX project_workers_project_id ON project_workers(project_id);
