-- The audit log is filtered by project, user and action and paged by (created_at, id).
CREATE INDEX audit_log_target ON audit_log(target_id, created_at);
CREATE INDEX audit_log_username ON audit_log(username, created_at);
CREATE INDEX audit_log_action ON audit_log(action, created_at);
