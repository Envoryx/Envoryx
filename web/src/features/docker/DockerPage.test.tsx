import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DockerPage } from "./DockerPage";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const orphan = { type: "volume", id: "envoryx-ghost-database", name: "envoryx-ghost-database", projectId: "ghost-id", projectName: "Ghost" };
const overview = {
  info: { connected: true, apiVersion: "1.47", serverVersion: "27.0", os: "linux", architecture: "x86_64", containers: 0, running: 0, ncpu: 4, memTotal: 8_000_000_000 },
  containers: [],
  foreign: [],
  networks: [],
  volumes: [{ Name: "envoryx-ghost-database", Driver: "local", Labels: {} }],
  orphans: [orphan],
  hostPath: { overrides: {}, detected: { "/projects": "/mnt/user/dev" }, bareMetal: false, selfContainerId: "abc" },
};

describe("DockerPage", () => {
  it("lists orphaned resources and removes one on request", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /docker": () => ({ body: overview }),
      "GET /docker/images/unused": () => ({ body: { images: [] } }),
      "POST /docker/orphans/remove": () => ({ body: { orphans: [] } }),
    });
    renderApp(<DockerPage />);
    const user = userEvent.setup();

    expect(await screen.findByText(/1 orphaned Envoryx resource/)).toBeInTheDocument();
    expect(screen.getByText(/volume envoryx-ghost-database/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/docker/orphans/remove"))).toBe(true));
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ type: "volume", id: "envoryx-ghost-database" });
  });

  it("groups Envoryx's containers by project and names the project of each volume", async () => {
    const container = (name: string, projectId: string, projectName: string, state: string) => ({ id: name, name, image: "caddy:2", state, status: "", created: "2026-09-29T10:00:00Z", managed: true, projectId, projectName, service: "web", ports: [] });
    mockApi({
      ...authedRoutes,
      "GET /docker": () => ({
        body: {
          ...overview,
          orphans: [],
          containers: [container("envoryx-shop-web", "p1", "Shop", "running"), container("envoryx-blog-web", "p2", "Blog", "exited"), container("envoryx-shop-php", "p1", "Shop", "running")],
          volumes: [{ Name: "envoryx-shop-database", Driver: "local", Labels: { "envoryx.project.id": "p1", "envoryx.project.name": "Shop" } }],
        },
      }),
    });
    renderApp(<DockerPage />, { route: "/docker?tab=containers" });
    const shop = (await screen.findByRole("link", { name: "Shop" })).closest("tbody") as HTMLElement;
    expect(within(shop).getByText("envoryx-shop-web")).toBeInTheDocument();
    expect(within(shop).getByText("envoryx-shop-php")).toBeInTheDocument();
    expect(within(shop).getByText("2 / 2 running")).toBeInTheDocument();
    expect(within(shop).queryByText("envoryx-blog-web")).not.toBeInTheDocument();

    await userEvent.setup().click(screen.getByRole("link", { name: "Networks & volumes" }));
    const volume = (await screen.findByText("envoryx-shop-database")).closest("li") as HTMLElement;
    expect(within(volume).getByRole("link", { name: "Shop" })).toHaveAttribute("href", "/projects/p1");
  });
});
