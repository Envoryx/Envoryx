-- Runs of a project's test suites from the Tests tab: the outcome and, as JSON, the
-- counts and failed cases read from the suite's JUnit report (store.TestRun).
CREATE TABLE project_test_runs (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    suite       TEXT NOT NULL,
    filter      TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    exit_code   INTEGER NOT NULL,
    started_at  TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    result      TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX project_test_runs_project ON project_test_runs(project_id, started_at);
