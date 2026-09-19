import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProjectDetailPage } from "./ProjectDetailPage";
import { authedRoutes, makeProject, mockApi, runtimesFixture, renderApp } from "@/test/utils";
import { Route, Routes } from "react-router-dom";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const proxy = { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true };

describe("Node dev server", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows the dev server link and saves dev-server options", async () => {
    const project = makeProject({
      devHostname: "acme-shop-dev.test",
      services: [
        { kind: "php", variant: "php", version: "8.4", image: "ghcr.io/envoryx/envoryx-php:8.4", enabled: true, config: { memoryLimit: "256M", uploadMaxFilesize: "64M", postMaxSize: "64M", maxExecutionTime: 120, displayErrors: true, errorReporting: "E_ALL", extensions: [] } },
        { kind: "web", variant: "caddy", version: "2", image: "caddy:2-alpine", enabled: true, config: {} },
        { kind: "node", variant: "node", version: "24", image: "ghcr.io/envoryx/envoryx-node:24", enabled: true, config: { devServer: true, packageManager: "npm", script: "dev", port: 5173, preset: "vite", hostPort: 20001 } },
      ],
    });
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: { publicHost: "", baseDomain: "test", forceHttps: false, proxy } }),
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET /projects/${id}/plan`]: () => ({ body: { preview: { containers: [] } } }),
      [`GET /projects/${id}/stats`]: () => ({ body: { stats: { cpuPercent: 1, memoryBytes: 1024, memoryLimit: 2048, containers: [] } } }),
      [`GET /projects/${id}`]: () => ({ body: { project } }),
      [`PATCH /projects/${id}`]: () => ({ body: { project } }),
    });
    renderApp(
      <Routes>
        <Route path="/projects/:id" element={<ProjectDetailPage />} />
      </Routes>,
      { route: `/projects/${id}` },
    );
    const user = userEvent.setup();
    expect((await screen.findAllByRole("link", { name: /https:\/\/acme-shop-dev\.test/ }))[0]).toHaveAttribute("href", "https://acme-shop-dev.test");

    await user.click(screen.getByRole("tab", { name: "Runtime" }));
    const preset = await screen.findByLabelText("Framework preset");
    await user.selectOptions(preset, "next");
    await user.selectOptions(screen.getByLabelText("Package manager"), "pnpm");
    await user.click(screen.getAllByRole("button", { name: "Save" }).at(-1)!);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      node: { enabled: true, version: "24", devServer: true, packageManager: "pnpm", script: "dev", port: 3000, preset: "next" },
    });
  });
});
