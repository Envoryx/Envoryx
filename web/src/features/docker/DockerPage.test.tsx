import { screen, waitFor } from "@testing-library/react";
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
});
