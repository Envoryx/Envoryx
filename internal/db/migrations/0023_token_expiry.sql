-- API tokens can expire. expires_at is NULL for a token that never does (every token
-- created before this migration); past it the token no longer signs in.
ALTER TABLE api_tokens ADD COLUMN expires_at TEXT;
