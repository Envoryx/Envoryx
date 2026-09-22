import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProjectsPage } from "./ProjectsPage";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

describe("ProjectsPage", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows an empty state", async () => {
    mockApi({
      ...authedRoutes,
      "GET /projects": () => ({ body: { projects: [] } }),
      "GET /dashboard": () => ({ status: 500, body: {} }),
    });
    renderApp(<ProjectsPage />);
    expect(await screen.findByText("No projects yet")).toBeInTheDocument();
  });

  it("lists projects and stops one", async () => {
    let project = makeProject();
    const api = mockApi({
      ...authedRoutes,
      "GET /projects": () => ({ body: { projects: [project] } }),
      "GET /dashboard": () => ({ status: 500, body: {} }),
      "POST /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/stop": () => {
        project = makeProject({ desiredState: "stopped", status: { ...project.status, state: "stopped", services: project.status.services.map((s) => ({ ...s, running: false, state: "exited" })) } });
        return { body: { project } };
      },
    });
    renderApp(<ProjectsPage />);
    const row = (await screen.findByText("Acme Shop")).closest("li")!;
    expect(within(row).getByText("PHP 8.4")).toBeInTheDocument();
    expect(within(row).getByText("Running")).toBeInTheDocument();
    expect(within(row).getByRole("link", { name: /Open/ })).toHaveAttribute("href", "http://localhost:20000");

    await userEvent.setup().click(within(row).getByRole("button", { name: "Stop" }));
    expect(await screen.findByText("Stopped")).toBeInTheDocument();
    expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/stop"))).toBe(true);
    expect(screen.getByRole("button", { name: "Start" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restart" })).not.toBeInTheDocument();
  });

  it("leads with the application runtime badge", async () => {
    const web = makeProject().services.find((s) => s.kind === "web")!;
    const node = { kind: "node", variant: "node", version: "24", image: "ghcr.io/envoryx/envoryx-node:24", enabled: true, config: { devServer: true } };
    const nodeOnly = makeProject({ id: "n1", name: "Vite App", slug: "vite-app", services: [web, node], serves: "node", appService: "node" });
    const staticSite = makeProject({ id: "s1", name: "Docs Site", slug: "docs-site", services: [web], serves: "static" });
    mockApi({
      ...authedRoutes,
      "GET /projects": () => ({ body: { projects: [makeProject(), nodeOnly, staticSite] } }),
      "GET /dashboard": () => ({ status: 500, body: {} }),
    });
    renderApp(<ProjectsPage />);
    const viteRow = (await screen.findByText("Vite App")).closest("li")!;
    expect(within(viteRow).getByText("Node.js 24")).toBeInTheDocument();
    const docsRow = screen.getByText("Docs Site").closest("li")!;
    expect(within(docsRow).getByText("Static")).toBeInTheDocument();
    expect(screen.queryByText("No PHP")).not.toBeInTheDocument();
    expect(within(screen.getByText("Acme Shop").closest("li")!).getByText("PHP 8.4")).toBeInTheDocument();
  });

  it("surfaces action errors", async () => {
    mockApi({
      ...authedRoutes,
      "GET /projects": () => ({ body: { projects: [makeProject()] } }),
      "GET /dashboard": () => ({ status: 500, body: {} }),
      "POST /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/restart": () => ({ status: 503, body: { error: { code: "docker_unavailable", message: "the Docker engine is not reachable" } } }),
    });
    renderApp(<ProjectsPage />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Restart" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("the Docker engine is not reachable");
  });
});
