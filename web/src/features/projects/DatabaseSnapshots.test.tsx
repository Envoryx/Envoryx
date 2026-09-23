import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CloneDatabaseCard, SnapshotsCard } from "./DatabaseSnapshots";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";
import type { DatabaseInfo, Project } from "@/api/types";

const shop = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

const database: DatabaseInfo = {
  type: "mariadb",
  version: "11",
  image: "mariadb:11",
  host: "database",
  port: 3306,
  database: "acme_shop",
  username: "acme_shop",
  hostPort: 0,
  injectedEnv: [],
  state: "running",
  volumeName: "envoryx-acme-shop-database",
  volumeExists: true,
};

const withDatabase = (overrides: Partial<Project> = {}) =>
  makeProject({
    services: [...makeProject().services, { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} }],
    ...overrides,
  });

const snapshot = {
  id: "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa",
  dir: "20260901-100000-abcdef12",
  kind: "database",
  sizeBytes: 2048,
  createdAt: "2026-09-01T10:00:00Z",
  missing: false,
  meta: { format: 1, envoryx: "test", projectId: shop, projectName: "Acme Shop", slug: "acme-shop", createdAt: "2026-09-01T10:00:00Z", note: "before the orders migration", source: "snapshot", database: { type: "mariadb", version: "11", name: "acme_shop", bytes: 2048 }, runtimes: {} },
};

describe("SnapshotsCard", () => {
  it("takes a snapshot with a note", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${shop}/database/snapshots`]: () => ({ body: { snapshots: [] } }),
      [`POST /projects/${shop}/database/snapshots`]: () => ({ status: 201, body: { snapshot: { ...snapshot, sizeBytes: 4096 } } }),
    });
    renderApp(<SnapshotsCard project={withDatabase()} database={database} onMessage={() => {}} />);
    const user = userEvent.setup();
    expect(await screen.findByText("No snapshots yet.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Note (optional)"), "before deploy");
    await user.click(screen.getByRole("button", { name: "Take snapshot" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/database/snapshots"))).toBe(true));
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ note: "before deploy" });
  });

  it("restores a snapshot only after the identifier is typed", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${shop}/database/snapshots`]: () => ({ body: { snapshots: [snapshot] } }),
      [`POST /projects/${shop}/database/snapshots/${snapshot.id}/restore`]: () => ({ body: { snapshot } }),
    });
    renderApp(<SnapshotsCard project={withDatabase()} database={database} onMessage={() => {}} />);
    const user = userEvent.setup();
    expect(await screen.findByText(/before the orders migration/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Restore" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "Restore" })).toBeDisabled();
    await user.type(within(dialog).getByLabelText("Type acme-shop to confirm"), "acme-shop");
    await user.click(within(dialog).getByRole("button", { name: "Restore" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/restore"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/restore"))!.body).toEqual({ confirm: "acme-shop" });
  });
});

describe("CloneDatabaseCard", () => {
  const staging = makeProject({
    id: "11111111-2222-4333-8444-555555555555",
    name: "Staging",
    slug: "staging",
    services: [{ kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} }],
  });

  it("offers only projects with the same engine and sends the clone with a snapshot", async () => {
    const postgres = makeProject({
      id: "99999999-2222-4333-8444-555555555555",
      name: "Reports",
      slug: "reports",
      services: [{ kind: "database", variant: "postgresql", version: "18", image: "postgres:18", enabled: true, config: {} }],
    });
    const api = mockApi({
      ...authedRoutes,
      "GET /projects": () => ({ body: { projects: [withDatabase(), staging, postgres] } }),
      [`POST /projects/${shop}/database/clone`]: () => ({ body: { clone: { source: "staging", database: "acme_shop", snapshot } } }),
    });
    renderApp(<CloneDatabaseCard project={withDatabase()} database={database} onMessage={() => {}} />);
    const user = userEvent.setup();
    const select = (await screen.findByLabelText("Source project")) as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["Staging (staging)"]);

    await user.click(screen.getByRole("button", { name: "Clone database" }));
    const dialog = screen.getByRole("dialog");
    await user.type(within(dialog).getByLabelText("Type acme-shop to confirm"), "acme-shop");
    await user.click(within(dialog).getByRole("button", { name: "Clone database" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/database/clone"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/database/clone"))!.body).toEqual({ source: staging.id, snapshot: true, confirm: "acme-shop" });
  });

  it("says so when no other project runs the same engine", async () => {
    mockApi({ ...authedRoutes, "GET /projects": () => ({ body: { projects: [withDatabase()] } }) });
    renderApp(<CloneDatabaseCard project={withDatabase()} database={database} onMessage={() => {}} />);
    expect(await screen.findByText("No other project runs mariadb, so there is nothing to clone from.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clone database" })).not.toBeInTheDocument();
  });
});
