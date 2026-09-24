-- Scheduled commands per project, run by Envoryx inside the application container.
CREATE TABLE project_cron_jobs (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    runtime         TEXT NOT NULL,
    schedule        TEXT NOT NULL,
    command         TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL DEFAULT 600,
    enabled         INTEGER NOT NULL DEFAULT 1,
    position        INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    UNIQUE(project_id, name)
);
CREATE INDEX project_cron_jobs_project_id ON project_cron_jobs(project_id);

-- The last runs of each job with the tail of their output.
CREATE TABLE project_cron_runs (
    id          TEXT PRIMARY KEY,
    job_id      TEXT NOT NULL REFERENCES project_cron_jobs(id) ON DELETE CASCADE,
    source      TEXT NOT NULL,
    status      TEXT NOT NULL,
    exit_code   INTEGER NOT NULL DEFAULT 0,
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT '',
    output      TEXT NOT NULL DEFAULT '',
    truncated   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX project_cron_runs_job ON project_cron_runs(job_id);
