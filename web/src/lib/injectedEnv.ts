/**
 * The variables Envoryx sets in the application containers for a project's services
 * (internal/runtime: DatabaseEnvFor, RedisEnv, MailpitEnv, …EnvKeys). A variable of the
 * project with the same name wins over them – which is what an imported .env of the old
 * setup (DB_HOST=127.0.0.1) must not do by accident.
 */

const databaseKeys = ["DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD", "DATABASE_URL", "MONGODB_URI", "MONGODB_DATABASE"];

const serviceKeys: Record<string, string[]> = {
  redis: ["REDIS_HOST", "REDIS_PORT", "REDIS_URL"],
  memcached: ["MEMCACHED_HOST", "MEMCACHED_PORT", "MEMCACHED_URL"],
  mailpit: ["MAIL_MAILER", "MAIL_HOST", "MAIL_PORT", "MAIL_ENCRYPTION", "MAILER_DSN", "SMTP_HOST", "SMTP_PORT"],
  rabbitmq: ["RABBITMQ_HOST", "RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASSWORD", "RABBITMQ_VHOST", "RABBITMQ_URL"],
  meilisearch: ["MEILISEARCH_HOST", "MEILISEARCH_KEY", "MEILISEARCH_URL", "MEILISEARCH_API_KEY"],
  typesense: ["TYPESENSE_HOST", "TYPESENSE_PORT", "TYPESENSE_PROTOCOL", "TYPESENSE_API_KEY", "TYPESENSE_URL"],
  opensearch: ["OPENSEARCH_HOST", "OPENSEARCH_PORT", "OPENSEARCH_SCHEME", "OPENSEARCH_URL"],
  ollama: ["OLLAMA_HOST", "OLLAMA_BASE_URL", "OLLAMA_URL"],
  storage: [
    "S3_ENDPOINT", "S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_USE_PATH_STYLE", "S3_PUBLIC_ENDPOINT", "S3_PUBLIC_URL",
    "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION", "AWS_BUCKET", "AWS_ENDPOINT", "AWS_USE_PATH_STYLE_ENDPOINT", "AWS_URL",
  ],
};

/**
 * Maps every variable Envoryx injects for these services to the service kind it comes
 * from ("database", "db-analytics", "redis" …).
 */
export function injectedEnv(services: { kind: string; enabled?: boolean }[]): Map<string, string> {
  const out = new Map<string, string>();
  for (const s of services) {
    if (s.enabled === false) continue;
    if (s.kind === "database") {
      for (const k of databaseKeys) out.set(k, s.kind);
    } else if (s.kind.startsWith("db-")) {
      const prefix = s.kind.slice(3).toUpperCase().replace(/-/g, "_");
      for (const k of databaseKeys) out.set(`${prefix}_${k}`, s.kind);
    } else {
      for (const k of serviceKeys[s.kind] ?? []) out.set(k, s.kind);
    }
  }
  return out;
}

/** Names the project cannot set itself (the server refuses them). */
export function reservedEnvKey(key: string): boolean {
  return /^(MARIADB_|MYSQL_|POSTGRES_|RUSTFS_|ENVORYX_)/.test(key);
}
