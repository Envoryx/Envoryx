import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BackupsTab } from "./BackupsTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const backup = {
  id: "b1", dir: "20260918-100000-abcd1234", kind: "full", sizeBytes: 2048, createdAt: "2026-09-18T10:00:00Z", missing: false,
  meta: { format: 1, envoryx: "dev", projectId: id, projectName: "Acme Shop", slug: "acme-shop", createdAt: "2026-09-18T10:00:00Z", note: "before deploy", database: { type: "mariadb", version: "11", name: "acme_shop", bytes: 1024 }, files: { bytes: 1024, entries: 12, includeDependencies: false }, runtimes: {} },
};

describe("BackupsTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("saves a weekly backup schedule", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/backups`]: () => ({ body: { backups: [] } }),
      [`PUT /projects/${id}/backups/schedule`]: (_u, init) => ({ body: { schedule: JSON.parse(init.body as string) } }),
    });
    renderApp(<BackupsTab project={makeProject()} />);
    const user = userEvent.setup();
    await user.selectOptions(await screen.findByLabelText("Frequency"), "weekly");
    await user.selectOptions(screen.getByLabelText("Weekday"), "6");
    await user.selectOptions(screen.getByLabelText("Time"), "2");
    await user.clear(screen.getByLabelText("Keep"));
    await user.type(screen.getByLabelText("Keep"), "4");
    await user.click(screen.getAllByRole("button", { name: "Save" })[0]!);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({ schedule: "weekly", hour: 2, weekday: 6, keep: 4, includeDependencies: false });
    expect(await screen.findByText("Schedule saved.")).toBeInTheDocument();
  });

  it("creates a backup and requires typed confirmation to restore", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/backups`]: () => ({ body: { backups: [backup] } }),
      [`POST /projects/${id}/backups/b1/restore`]: () => ({ body: { backup } }),
      [`POST /projects/${id}/backups`]: () => ({ status: 201, body: { backup } }),
    });
    const project = makeProject({ services: [...makeProject().services, { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} }] });
    renderApp(<BackupsTab project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByText(/before deploy/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create backup" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/backups"))).toBe(true));
    expect(api.calls.find((c) => c.method === "POST" && c.url.endsWith("/backups"))!.body).toEqual({ database: true, files: true, storage: false, includeDependencies: false, note: "" });

    await user.click(screen.getByRole("button", { name: "Restore" }));
    expect(await screen.findByText(/This overwrites current data/)).toBeInTheDocument();
    const dialogButton = () => screen.getAllByRole("button", { name: "Restore" }).at(-1) as HTMLButtonElement;
    expect(dialogButton()).toBeDisabled();
    await user.type(screen.getByLabelText("Type acme-shop to confirm"), "acme-shop");
    await waitFor(() => expect(dialogButton()).not.toBeDisabled());
    await user.click(dialogButton());
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/restore"))).toBe(true));
    expect(api.calls.find((c) => c.url.endsWith("/restore"))!.body).toEqual({ database: true, files: true, storage: false, wipeFiles: false, wipeStorage: false, confirm: "acme-shop" });
  });

  it("backs up and restores addon volumes in a project without a database", async () => {
    const withAddon = { ...backup, meta: { ...backup.meta, database: undefined, addonVolumes: ["addon-keycloak-data.tar.gz"] } };
    const api = mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/backups`]: () => ({ body: { backups: [withAddon] } }),
      [`POST /projects/${id}/backups/b1/restore`]: () => ({ body: { backup: withAddon } }),
      [`POST /projects/${id}/backups`]: () => ({ status: 201, body: { backup: withAddon } }),
    });
    const project = makeProject({ services: [...makeProject().services, { kind: "addon-keycloak", variant: "keycloak", version: "26", image: "quay.io/keycloak/keycloak:26.7", enabled: true, config: { backupVolumes: 1 } }] });
    renderApp(<BackupsTab project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByText("The project has no database; the addon volumes are saved.")).toBeInTheDocument();
    const db = screen.getByRole("checkbox", { name: /^Database/ });
    expect(db).toBeEnabled();
    expect(db).toBeChecked();
    await user.click((await screen.findAllByRole("button", { name: "Restore" }))[0]!);
    expect(await screen.findByText("With the addon volumes: addon-keycloak-data.tar.gz")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /^Restore database/ })).toBeEnabled();
    void api;
  });

  it("shows a viewer the backups but nothing to create, download, restore or delete", async () => {
    mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/backups`]: () => ({ body: { backups: [backup], offsiteTargets: [{ id: "t1", name: "NAS", enabled: true }] } }),
    });
    renderApp(<BackupsTab project={makeProject({ access: "read" })} />);

    expect(await screen.findByText(/before deploy/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create backup" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Download/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete backup" })).not.toBeInTheDocument();
    expect(screen.queryByText("Offsite copies")).not.toBeInTheDocument();
    // The schedule stays readable but cannot be saved.
    expect(screen.getByLabelText("Frequency")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });

  it("lets a developer create and download backups but not restore or delete them", async () => {
    mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/backups`]: () => ({ body: { backups: [backup] } }),
    });
    renderApp(<BackupsTab project={makeProject({ access: "operate" })} />);

    expect(await screen.findByText(/before deploy/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create backup" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Download/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete backup" })).not.toBeInTheDocument();
  });

  it("lists and restores the copy of a data volume taken before an upgrade", async () => {
    const copy = {
      ...backup,
      kind: "database",
      meta: { ...backup.meta, source: "upgrade", database: undefined, files: undefined, databaseVolumes: [{ type: "mongodb", version: "8", file: "database.volume.tar.gz", bytes: 4096 }] },
    };
    mockApi({
      ...authedRoutes,
      [`GET /projects/${id}/backups`]: () => ({ body: { backups: [copy] } }),
    });
    const project = makeProject({ services: [...makeProject().services, { kind: "database", variant: "mongodb", version: "8.2", image: "mongo:8.2", enabled: true, config: {} }] });
    renderApp(<BackupsTab project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByText(/mongodb 8 · volume copy · 4/)).toBeInTheDocument();
    await user.click((await screen.findAllByRole("button", { name: "Restore" }))[0]!);
    expect(await screen.findByText("The data volume of the primary database is replaced by the copy; changes since the backup are lost.")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /^Restore database/ })).toBeEnabled();
  });
});
