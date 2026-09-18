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
    const body = init?.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ method, url, body });
    const key = Object.keys(routes).find((k) => {
      const [m, p] = k.split(" ");
      return m === method && url.startsWith(`/api/v1${p}`);
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

export function renderApp(ui: ReactNode, { route = "/" }: { route?: string } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, refetchInterval: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[route]}>
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
    name: "Shimly API",
    slug: "shimly-api",
    path: "shimly-api",
    docroot: "public",
    desiredState: "running",
    lifecycle: "ready",
    httpPort: 20000,
    createdAt: "2026-09-17T10:00:00Z",
    updatedAt: "2026-09-17T10:00:00Z",
    services: [
      { kind: "php", variant: "php", version: "8.4", image: "ghcr.io/seramos/staqio-php:8.4", enabled: true, config: {} },
      { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", enabled: true, config: {} },
    ],
    env: [],
    git: { url: "", branch: "", username: "", hasToken: false },
    hostnames: ["shimly-api.test"],
    status: {
      state: "running",
      services: [
        { kind: "php", variant: "php", version: "8.4", image: "ghcr.io/seramos/staqio-php:8.4", containerName: "staqio-shimly-api-php", exists: true, running: true, state: "running", ports: [] },
        { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", containerName: "staqio-shimly-api-web", exists: true, running: true, state: "running", ports: [{ hostIp: "", hostPort: 20000, containerPort: 80, protocol: "tcp" }] },
      ],
      warnings: [],
    },
    ...overrides,
  };
}

export const runtimesFixture: RuntimesResponse = {
  runtimes: [
    { key: "php", name: "PHP", kind: "runtime", available: true, description: "", versions: [{ version: "8.4", image: "ghcr.io/seramos/staqio-php:8.4", label: "PHP 8.4", default: true }, { version: "8.3", image: "ghcr.io/seramos/staqio-php:8.3", label: "PHP 8.3" }] },
    { key: "caddy", name: "Caddy", kind: "webserver", available: true, description: "", versions: [{ version: "2", image: "caddy:2-alpine", label: "Caddy 2", default: true }] },
    { key: "node", name: "Node.js", kind: "runtime", available: true, description: "", versions: [{ version: "24", image: "ghcr.io/seramos/staqio-node:24", label: "Node 24 LTS", default: true }, { version: "22", image: "ghcr.io/seramos/staqio-node:22", label: "Node 22 LTS" }] },
    { key: "mariadb", name: "MariaDB", kind: "database", available: true, description: "", versions: [{ version: "11", image: "mariadb:11", label: "MariaDB 11", default: true }, { version: "10.11", image: "mariadb:10.11", label: "MariaDB 10.11" }] },
    { key: "redis", name: "Redis", kind: "service", available: true, description: "", versions: [{ version: "8", image: "redis:8", label: "Redis 8", default: true }] },
    { key: "mailpit", name: "Mailpit", kind: "service", available: true, description: "", versions: [{ version: "1.31", image: "axllent/mailpit:v1.31", label: "Mailpit 1.31", default: true }] },
  ],
  phpExtensions: [
    { name: "mbstring", description: "Multibyte", builtIn: true, available: true },
    { name: "opcache", description: "Opcode cache", builtIn: false, available: true },
    { name: "gd", description: "Images", builtIn: false, available: false },
  ],
  phpDefaults: { memoryLimit: "256M", uploadMaxFilesize: "64M", postMaxSize: "64M", maxExecutionTime: 120, displayErrors: true, errorReporting: "E_ALL", extensions: ["opcache"] },
};
