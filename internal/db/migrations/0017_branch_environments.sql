-- Branch environments: a project that is a copy of another project on one branch of its
-- repository names that project in parent_id ('' for every other project). The parent
-- keeps its branch settings (store.BranchSettings) and each environment its deploy state
-- (store.BranchState), both as JSON; '' = none.
ALTER TABLE projects ADD COLUMN parent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN branch_settings TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN branch_state TEXT NOT NULL DEFAULT '';
CREATE INDEX projects_parent ON projects(parent_id);
