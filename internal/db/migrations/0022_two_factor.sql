-- Two-factor authentication. A user signing in with a password proves a second factor:
-- a TOTP code (the secret sealed like the other secrets; last_step keeps a code from
-- being used twice), a passkey (WebAuthn; credential is the library's JSON of it, rp_id
-- the host name it was registered for), or one of the one-time recovery codes (only
-- their hashes are kept). A passkey also signs in on its own. sessions.method says how a
-- session was started (password, totp, passkey, recovery, oidc, support, invite; empty
-- for sessions from before), which decides whether the instance's 2FA requirement
-- applies to it.
CREATE TABLE user_totp (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    secret     TEXT NOT NULL,
    last_step  INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE TABLE user_passkeys (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    rp_id        TEXT NOT NULL,
    credential   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX user_passkeys_user ON user_passkeys(user_id);

CREATE TABLE user_recovery_codes (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    hash    TEXT NOT NULL,
    PRIMARY KEY (user_id, hash)
);

ALTER TABLE sessions ADD COLUMN method TEXT NOT NULL DEFAULT '';
