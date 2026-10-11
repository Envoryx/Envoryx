-- The hoster's support signs in as a user of its own, which only exists on a managed
-- instance whose admins allowed support access. It counts towards no limit, can't sign
-- in with a password and is left out of the user list.
ALTER TABLE users ADD COLUMN support INTEGER NOT NULL DEFAULT 0;
