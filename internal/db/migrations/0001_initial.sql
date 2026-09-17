CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'admin',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id ON sessions(user_id);
CREATE INDEX sessions_expires_at ON sessions(expires_at);

CREATE TABLE projects (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE COLLATE NOCASE,
    slug          TEXT NOT NULL UNIQUE,
    path          TEXT NOT NULL UNIQUE,
    docroot       TEXT NOT NULL DEFAULT '',
    desired_state TEXT NOT NULL DEFAULT 'stopped',
    http_port     INTEGER UNIQUE,
    lifecycle     TEXT NOT NULL DEFAULT 'creating',
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE project_services (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    variant    TEXT NOT NULL DEFAULT '',
    version    TEXT NOT NULL DEFAULT '',
    image      TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    config     TEXT NOT NULL DEFAULT '{}',
    position   INTEGER NOT NULL DEFAULT 0,
    UNIQUE(project_id, kind)
);

CREATE TABLE project_environment_variables (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    is_secret  INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    UNIQUE(project_id, key)
);

CREATE TABLE domains (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    hostname   TEXT NOT NULL UNIQUE COLLATE NOCASE,
    is_primary INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE TABLE backups (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    filename   TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    kind       TEXT NOT NULL,
    metadata   TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX backups_project_id ON backups(project_id);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE audit_log (
    id          TEXT PRIMARY KEY,
    created_at  TEXT NOT NULL,
    user_id     TEXT NOT NULL DEFAULT '',
    username    TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    details     TEXT NOT NULL DEFAULT '{}',
    ip          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_created_at ON audit_log(created_at);
