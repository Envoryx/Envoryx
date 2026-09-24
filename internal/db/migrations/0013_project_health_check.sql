-- HTTP health check of the project's application as JSON (store.HealthCheck); '' = none.
ALTER TABLE projects ADD COLUMN health_check TEXT NOT NULL DEFAULT '';
