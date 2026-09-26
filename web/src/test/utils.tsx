import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "@/features/auth/AuthContext";
import type { Project, RuntimesResponse } from "@/api/types";

type Handler = (url: string, init: RequestInit) => { status?: number; body?: unknown } | Promise<{ status?: number; body?: unknown }>;

/** Installs a fetch mock. Handlers are matched by "METHOD /path" prefix. */
export function mockApi(routes: Record<string, Handler>) {
  const calls: { method: string; url: string; body: unknown }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    const method = (init?.method ?? "GET").toUpperCase();
    const body = init?.body instanceof FormData ? init.body : init?.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ method, url, body });
    const key = Object.keys(routes).find((k) => {
      const [m, p] = k.split(" ");
      // API routes are relative to /api/v1; absolute keys match other origins (probes).
      return m === method && (p!.startsWith("http") ? url.startsWith(p!) : url.startsWith(`/api/v1${p}`));
    });
    if (!key) {
      return new Response(JSON.stringify({ error: { code: "not_found", message: `no mock for ${method} ${url}` } }), { status: 404 });
    }
    const res = await routes[key]!(url, init ?? {});
    const status = res.status ?? 200;
    if (status === 204) return new Response(null, { status });
    return new Response(JSON.stringify(res.body ?? {}), { status, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
  return { calls, fetchMock };
}

export function renderApp(ui: ReactNode, { route = "/", state }: { route?: string; state?: unknown } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, refetchInterval: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[{ pathname: route, state }]}>
        <AuthProvider>{ui}</AuthProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

export const authedRoutes = {
  "GET /setup": () => ({ body: { needsSetup: false } }),
  "GET /auth/me": () => ({ body: { user: { id: "u1", username: "admin", role: "admin" } } }),
};

export function makeProject(overrides: Partial<Project> = {}): Project {
  return {
    id: "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f",
    name: "Acme Shop",
    slug: "acme-shop",
    path: "acme-shop",
    docroot: "public",
    serves: "php",
    appService: "php",
    desiredState: "running",
    lifecycle: "ready",
    httpPort: 20000,
    createdAt: "2026-09-17T10:00:00Z",
    updatedAt: "2026-09-17T10:00:00Z",
    services: [
      { kind: "php", variant: "php", version: "8.4", image: "ghcr.io/envoryx/envoryx-php:8.4", enabled: true, config: {} },
      { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", enabled: true, config: {} },
    ],
    env: [],
    git: { url: "", branch: "", username: "", hasToken: false },
    hostnames: ["acme-shop.test"],
    backupSchedule: { schedule: "", hour: 3, weekday: 0, keep: 7, includeDependencies: false },
    status: {
      state: "running",
      services: [
        { kind: "php", variant: "php", version: "8.4", image: "ghcr.io/envoryx/envoryx-php:8.4", containerName: "envoryx-acme-shop-php", exists: true, running: true, state: "running", ports: [], imagePrevious: false, imagePinned: false },
        { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", containerName: "envoryx-acme-shop-web", exists: true, running: true, state: "running", ports: [{ hostIp: "", hostPort: 20000, containerPort: 80, protocol: "tcp" }], imagePrevious: false, imagePinned: false },
      ],
      warnings: [],
    },
    ...overrides,
  };
}

export const runtimesFixture: RuntimesResponse = {
  templates: [
    { id: "laravel", name: "Laravel", runtime: "php", description: "composer create-project laravel/laravel", docroot: "public", requiresDatabase: false, recommendedDatabase: "mariadb" },
    { id: "wordpress", name: "WordPress", runtime: "php", description: "Latest WordPress", docroot: "", requiresDatabase: true, recommendedDatabase: "mariadb", phpExtensions: ["mysqli"] },
  ],
  runtimes: [
    { key: "php", name: "PHP", kind: "runtime", available: true, description: "", versions: [{ version: "8.4", image: "ghcr.io/envoryx/envoryx-php:8.4", label: "PHP 8.4", default: true }, { version: "8.3", image: "ghcr.io/envoryx/envoryx-php:8.3", label: "PHP 8.3" }] },
    { key: "caddy", name: "Caddy", kind: "webserver", available: true, description: "", versions: [{ version: "2", image: "caddy:2-alpine", label: "Caddy 2", default: true }] },
    { key: "apache", name: "Apache", kind: "webserver", available: true, description: "", versions: [{ version: "2.4", image: "httpd:2.4-alpine", label: "Apache 2.4", default: true }] },
    { key: "nginx", name: "Nginx", kind: "webserver", available: true, description: "", versions: [{ version: "1", image: "nginx:1-alpine", label: "Nginx 1", default: true }] },
    { key: "node", name: "Node.js", kind: "runtime", available: true, description: "", versions: [{ version: "24", image: "ghcr.io/envoryx/envoryx-node:24", label: "Node 24 LTS", default: true }, { version: "22", image: "ghcr.io/envoryx/envoryx-node:22", label: "Node 22 LTS" }] },
    { key: "python", name: "Python", kind: "runtime", available: true, description: "", versions: [{ version: "3.13", image: "ghcr.io/envoryx/envoryx-python:3.13", label: "Python 3.13", default: true }, { version: "3.12", image: "ghcr.io/envoryx/envoryx-python:3.12", label: "Python 3.12" }] },
    { key: "mariadb", name: "MariaDB", kind: "database", available: true, description: "", versions: [{ version: "11", image: "mariadb:11", label: "MariaDB 11", default: true }, { version: "10.11", image: "mariadb:10.11", label: "MariaDB 10.11" }] },
    { key: "redis", name: "Redis", kind: "service", available: true, description: "", versions: [{ version: "8", image: "redis:8", label: "Redis 8", default: true }] },
    { key: "memcached", name: "Memcached", kind: "service", available: true, description: "", versions: [{ version: "1.6", image: "memcached:1.6-alpine", label: "Memcached 1.6", default: true }] },
    { key: "rabbitmq", name: "RabbitMQ", kind: "service", available: true, description: "", versions: [{ version: "4.3", image: "rabbitmq:4.3-management-alpine", label: "RabbitMQ 4.3", default: true }] },
    { key: "meilisearch", name: "Meilisearch", kind: "service", available: true, description: "", versions: [{ version: "1.54", image: "getmeili/meilisearch:v1.54", label: "Meilisearch 1.54", default: true }] },
    { key: "typesense", name: "Typesense", kind: "service", available: true, description: "", versions: [{ version: "30.2", image: "typesense/typesense:30.2", label: "Typesense 30.2", default: true }] },
    { key: "opensearch", name: "OpenSearch", kind: "service", available: true, description: "", versions: [{ version: "3.8", image: "opensearchproject/opensearch:3.8.0", label: "OpenSearch 3.8", default: true }, { version: "2.19", image: "opensearchproject/opensearch:2.19.6", label: "OpenSearch 2.19" }] },
    { key: "opensearch-dashboards", name: "OpenSearch Dashboards", kind: "service", available: true, description: "", versions: [{ version: "3.8", image: "opensearchproject/opensearch-dashboards:3.8.0", label: "OpenSearch Dashboards 3.8", default: true }, { version: "2.19", image: "opensearchproject/opensearch-dashboards:2.19.6", label: "OpenSearch Dashboards 2.19" }] },
    { key: "mailpit", name: "Mailpit", kind: "service", available: true, description: "", versions: [{ version: "1.31", image: "axllent/mailpit:v1.31", label: "Mailpit 1.31", default: true }] },
    { key: "ollama", name: "Ollama", kind: "service", available: true, description: "", versions: [{ version: "0.34", image: "ollama/ollama:0.34.4", label: "Ollama 0.34", default: true }] },
  ],
  phpExtensions: [
    { name: "mbstring", description: "Multibyte", builtIn: true, available: true },
    { name: "opcache", description: "Opcode cache", builtIn: false, available: true },
    { name: "gd", description: "Images", builtIn: false, available: false },
  ],
  phpDefaults: { memoryLimit: "256M", uploadMaxFilesize: "64M", postMaxSize: "64M", maxExecutionTime: 120, displayErrors: true, errorReporting: "E_ALL", extensions: ["opcache"] },
};
