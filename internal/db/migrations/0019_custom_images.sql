-- A runtime service may run an image of the user's instead of the catalogue's: a
-- registry reference or a Dockerfile in the project that Envoryx builds. custom_image
-- holds it as JSON (store.CustomImage); '' = the catalogue image.
ALTER TABLE project_services ADD COLUMN custom_image TEXT NOT NULL DEFAULT '';
