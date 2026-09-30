-- A service removed without its data (Redis, RabbitMQ, Meilisearch, Typesense,
-- OpenSearch) leaves its volume behind. Its settings stay here, config sealed like
-- project_services.config, so adding the service again reuses the credentials the data
-- was initialised with. The row goes when the service comes back, when the kept data is
-- deleted, or with the project.
CREATE TABLE project_kept_services (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    version    TEXT NOT NULL DEFAULT '',
    config     TEXT NOT NULL DEFAULT '{}',
    kept_at    TEXT NOT NULL,
    UNIQUE(project_id, kind)
);
