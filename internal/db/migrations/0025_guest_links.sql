-- Guest links let someone without an Envoryx account see a project that signs in through
-- Envoryx: a customer's customer reviewing a staging site. Only the token's hash is kept;
-- a link ends at expires_at or when revoked (the row goes).
CREATE TABLE guest_links (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT
);
CREATE INDEX guest_links_project ON guest_links (project_id);
