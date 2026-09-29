import { screen, within } from "@testing-library/react";
import { DashboardPage } from "./DashboardPage";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const shop = makeProject({ status: { ...makeProject().status, state: "running" } });
const dashboard = {
  projects: { total: 3, running: 1, stopped: 2, attention: 0 },
  docker: { connected: true, serverVersion: "29.1", apiVersion: "1.56", os: "linux", architecture: "x86_64", containers: 5, running: 2, ncpu: 8, memTotal: 16_000_000_000 },
  stats: { containers: 2, running: 2, cpuPercent: 12, memoryBytes: 2_000_000_000, perProject: { [shop.id]: { cpuPercent: 3, memoryBytes: 180_000_000 } }, sampledAt: "2026-09-29T12:00:00Z" },
  recent: [shop],
  issues: [{ projectId: shop.id, projectName: "Acme Shop", severity: "warning", message: "container missing" }],
  orphans: 2,
  activity: [],
  hostPath: { overrides: {}, detected: {}, bareMetal: true },
  storage: [{ path: "/projects", totalBytes: 100, freeBytes: 40, low: false }],
  version: "0.15.0",
  update: { available: true, current: "0.15.0", latest: "0.16.0", url: "https://example.test/notes" },
  publicHost: "",
  baseDomain: "test",
  proxy: { enabled: false, httpPort: 0, httpsPort: 0, inDocker: false, tls: false },
};
const viewerMe = { "GET /auth/me": () => ({ body: { user: { id: "u2", username: "vera", role: "viewer" } } }) };

describe("DashboardPage", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("gathers the notices in one box and lists the projects with their actions", async () => {
    mockApi({
      ...authedRoutes,
      "GET /dashboard": () => ({ body: dashboard }),
      "GET /system/diagnostics": () => ({ body: { checks: [{ id: "x", category: "docker", status: "warning", title: "Disk space" }], summary: { ok: 0, info: 0, warning: 1, error: 0 }, at: "2026-09-29T12:00:00Z" } }),
      "GET /settings": () => ({ body: { publicHost: "", baseDomain: "test", forceHttps: false, proxy: dashboard.proxy } }),
    });
    renderApp(<DashboardPage />);
    expect(await screen.findByText("Envoryx 0.16.0 is available")).toBeInTheDocument();
    expect(await screen.findByText("Set-up check: 1 warning")).toBeInTheDocument();
    expect(screen.getByText(/^1 inconsistenc/)).toBeInTheDocument();

    const projects = screen.getByRole("heading", { name: "Projects" }).closest(".rounded-xl") as HTMLElement;
    expect(within(projects).getByText("1 of 3 running")).toBeInTheDocument();
    expect(within(projects).getByRole("link", { name: "Acme Shop" })).toHaveAttribute("href", `/projects/${shop.id}`);
    expect(within(projects).getByRole("button", { name: "Stop" })).toBeInTheDocument();
    expect(within(projects).getAllByText(/^CPU 3/).length).toBeGreaterThan(0);

    const system = screen.getByRole("heading", { name: "System" }).closest(".rounded-xl") as HTMLElement;
    expect(within(system).getByText("29.1")).toBeInTheDocument();
    expect(within(system).getByText("40 B free")).toBeInTheDocument();
    expect(within(system).getByRole("link", { name: /Docker/ })).toHaveAttribute("href", "/docker");
    expect(within(system).getByRole("link", { name: "2" })).toHaveAttribute("href", "/docker");
  });

  it("links a viewer neither to the Docker page nor to the diagnostics", async () => {
    mockApi({
      ...authedRoutes,
      ...viewerMe,
      "GET /dashboard": () => ({ body: { ...dashboard, update: undefined, issues: [] } }),
      "GET /settings": () => ({ body: { publicHost: "", baseDomain: "test", forceHttps: false, proxy: dashboard.proxy } }),
    });
    renderApp(<DashboardPage />);
    const system = (await screen.findByRole("heading", { name: "System" })).closest(".rounded-xl") as HTMLElement;
    expect(within(system).queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByText(/Set-up check/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New project" })).not.toBeInTheDocument();
  });
});
