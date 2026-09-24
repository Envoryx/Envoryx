-- Copies of backups on offsite targets (S3, SFTP, WebDAV). One row per backup and
-- target; the target configuration itself lives in /config/offsite.json.
CREATE TABLE offsite_uploads (
    id              TEXT PRIMARY KEY,
    target_id       TEXT NOT NULL,
    scope           TEXT NOT NULL,              -- project | instance
    project_id      TEXT NOT NULL DEFAULT '',   -- set for project backups
    backup_id       TEXT NOT NULL,              -- project backup id or instance backup id
    source          TEXT NOT NULL DEFAULT '',   -- scheduled (rotated on the target) | manual
    status          TEXT NOT NULL,              -- pending | running | done | failed
    remote_key      TEXT NOT NULL DEFAULT '',
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    error           TEXT NOT NULL DEFAULT '',
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE(target_id, scope, backup_id)
);
CREATE INDEX offsite_uploads_status ON offsite_uploads(status);
CREATE INDEX offsite_uploads_backup ON offsite_uploads(scope, backup_id);
