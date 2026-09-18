-- Personal API tokens for MCP / automation clients. The token itself is never stored,
-- only its SHA-256 hash; prefix is a short, non-secret identifier for the UI.
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT
);
CREATE INDEX api_tokens_user_id ON api_tokens(user_id);
