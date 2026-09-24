import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ResourcesTab } from "./ResourcesTab";
import { ResourceOverviewCard } from "@/features/dashboard/ResourceOverviewCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const now = Math.floor(Date.now() / 1000);
const pt = (ts: number, cpu: number, mem: number, rx = 0) => [ts, cpu, cpu, mem, mem, rx, rx / 2, 0, 1024];

describe("ResourcesTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("draws the history per container and switches range and table", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/metrics`]: (url: string) => ({
        body: {
          metrics: {
            from: now - 3600,
            to: now,
            res: url.includes("range=1h") ? 60 : 300,
            containers: [
              { name: "database", group: "services", points: [pt(now - 120, 10, 300 << 20), pt(now - 60, 12, 310 << 20)] },
              { name: "php", group: "app", points: [pt(now - 120, 150, 100 << 20, 2048), pt(now - 60, 50, 120 << 20, 4096)] },
            ],
            sizes: [{ kind: "volume", name: "envoryx-acme-shop-database", points: [[now - 3600, 50 << 20]] }],
          },
        },
      }),
      [`GET /projects/${id}/stats`]: () => ({ body: { stats: { cpuPercent: 0, memoryBytes: 0, running: 0, containers: 0 }, sampledAt: "", containers: [] } }),
    });
    renderApp(<ResourcesTab project={makeProject({ id })} />);
    const user = userEvent.setup();

    expect(await screen.findByRole("img", { name: "CPU" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Memory" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Disk space" })).toBeInTheDocument();
    // Two containers: a legend with both.
    expect(screen.getAllByText("php").length).toBeGreaterThan(0);
    expect(screen.getAllByText("database").length).toBeGreaterThan(0);
    expect(screen.getByText("Averages over 5 minutes")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "Table" })[0]!);
    expect(screen.getByRole("columnheader", { name: "php" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "1.5" })).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "1 hour" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.includes("range=1h"))).toBe(true));
    expect(await screen.findByText("One value per minute")).toBeInTheDocument();
  });

  it("says so when nothing is recorded yet", async () => {
    mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/metrics`]: () => ({ body: { metrics: { from: now - 86400, to: now, res: 300, containers: [], sizes: [] } } }),
      [`GET /projects/${id}/stats`]: () => ({ body: { stats: { cpuPercent: 0, memoryBytes: 0, running: 0, containers: 0 }, sampledAt: "", containers: [] } }),
    });
    renderApp(<ResourcesTab project={makeProject({ id })} />);
    expect(await screen.findByText(/Nothing recorded yet/)).toBeInTheDocument();
  });
});

describe("ResourceOverviewCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("lists the projects busiest first and links to their history", async () => {
    mockApi({
      ...authedRoutes,
      "GET /metrics/overview": () => ({
        body: {
          overview: {
            from: now - 86400,
            to: now,
            res: 300,
            projects: [
              { id: "p1", name: "Shop", slug: "shop", cpuAvg: 150, cpuMax: 300, cpuNow: 120, memAvg: 512 << 20, memMax: 1 << 30, memNow: 400 << 20, netRx: 0, netTx: 0, blkRead: 0, blkWrite: 0, disk: { volume: 1 << 30, files: 100 << 20 }, series: [[now - 600, 100, 0], [now - 300, 150, 0]] },
              { id: "p2", name: "Blog", slug: "blog", cpuAvg: 5, cpuMax: 10, cpuNow: 5, memAvg: 64 << 20, memMax: 80 << 20, memNow: 64 << 20, netRx: 0, netTx: 0, blkRead: 0, blkWrite: 0, disk: { files: 1 << 20 }, series: [[now - 300, 5, 0]] },
              { id: "p3", name: "Idle", slug: "idle", cpuAvg: 0, cpuMax: 0, cpuNow: 0, memAvg: 0, memMax: 0, memNow: 0, netRx: 0, netTx: 0, blkRead: 0, blkWrite: 0, disk: {}, series: [] },
            ],
          },
        },
      }),
    });
    renderApp(<ResourceOverviewCard />);
    const shop = await screen.findByRole("link", { name: "Shop" });
    expect(shop).toHaveAttribute("href", "/projects/p1?tab=Resources");
    expect(screen.getByText("1.50")).toBeInTheDocument();
    expect(screen.queryByText("Idle")).not.toBeInTheDocument();
    const rows = screen.getAllByRole("row");
    expect(rows[1]).toHaveTextContent("Shop");
    expect(rows[2]).toHaveTextContent("Blog");
  });
});
