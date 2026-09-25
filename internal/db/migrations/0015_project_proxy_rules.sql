-- Rules the proxy applies to a project's host names as JSON (store.ProxyRules); '' = none.
ALTER TABLE projects ADD COLUMN proxy_rules TEXT NOT NULL DEFAULT '';
