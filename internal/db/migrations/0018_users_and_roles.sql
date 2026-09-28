-- Users and roles: a user's role is admin, developer, viewer or none (see auth.Role),
-- project_roles give a user another role in particular projects. A user invited but
-- not yet signed up has no password and an invite hash with its expiry; a user signing
-- in through OpenID Connect is linked by the provider's subject. A disabled user can
-- neither sign in nor use its tokens. ssh_keys are the user's own public keys for the
-- SSH server (authorized_keys format), which act with the user's roles.
ALTER TABLE users ADD COLUMN invite_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN invite_expires_at TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN ssh_keys TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX users_invite_hash ON users(invite_hash) WHERE invite_hash <> '';
CREATE UNIQUE INDEX users_oidc_subject ON users(oidc_subject) WHERE oidc_subject <> '';

CREATE TABLE project_roles (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    PRIMARY KEY (user_id, project_id)
);
CREATE INDEX project_roles_project ON project_roles(project_id);
