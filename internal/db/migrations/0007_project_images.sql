-- Image history per project and image reference: the id the containers were last
-- (re)created with, the id before that, and whether the project is pinned to the
-- previous one (rollback after a broken upstream rebuild of the same tag).
CREATE TABLE project_images (
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    image       TEXT NOT NULL,
    current_id  TEXT NOT NULL,
    previous_id TEXT NOT NULL DEFAULT '',
    pinned      INTEGER NOT NULL DEFAULT 0,
    changed_at  TEXT NOT NULL,
    PRIMARY KEY (project_id, image)
);
