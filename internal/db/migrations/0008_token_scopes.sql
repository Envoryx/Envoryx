-- Token scopes: what a token may do (read < operate < admin) and, optionally, which
-- projects it is confined to (JSON array of project ids; empty = all). Tokens issued
-- before this migration keep the full access they had.
ALTER TABLE api_tokens ADD COLUMN scope TEXT NOT NULL DEFAULT 'admin';
ALTER TABLE api_tokens ADD COLUMN project_ids TEXT NOT NULL DEFAULT '[]';
