import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DatabaseTab } from "./DatabaseTab";
import { authedRoutes, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";

const P = "/projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

const withDb = () =>
  makeProject({
    services: [
      ...makeProject().services,
      { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: { database: "acme_shop", username: "acme_shop", hostPort: 0 } },
    ],
  });

describe("DatabaseTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("offers to add a database when none exists", async () => {
    mockApi({ ...authedRoutes, "GET /runtimes": () => ({ body: runtimesFixture }) });
    renderApp(<DatabaseTab project={makeProject()} />);
    expect(await screen.findByRole("button", { name: "Add database" })).toBeInTheDocument();
  });

  it("shows connection info, reveals credentials only on request and manages databases", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "GET /settings": () => ({ body: { publicHost: "192.168.1.10" } }),
      "GET /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/database/credentials": () => ({
        body: { credentials: { host: "database", port: 3306, database: "acme_shop", username: "acme_shop", password: "s3cretPassword", rootPassword: "r00tPassword", hostPort: 0, url: "mysql://acme_shop:s3cretPassword@database:3306/acme_shop" } },
      }),
      "GET /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/database/databases": () => ({ body: { databases: ["reports", "acme_shop"] } }),
      "POST /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/database/databases": () => ({ status: 201 }),
      "GET /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/database": () => ({
        body: { database: { type: "mariadb", version: "11", image: "mariadb:11", host: "database", port: 3306, database: "acme_shop", username: "acme_shop", hostPort: 0, injectedEnv: ["DB_HOST", "DB_PASSWORD"], state: "running", health: "healthy", volumeName: "envoryx-acme-shop-database", volumeExists: true } },
      }),
    });
    renderApp(<DatabaseTab project={withDb()} />);
    const user = userEvent.setup();

    expect((await screen.findAllByText("acme_shop", { selector: "dd" })).length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("healthy")).toBeInTheDocument();
    // No credentials request before the user asks.
    expect(api.calls.some((c) => c.url.endsWith("/credentials"))).toBe(false);
    await user.click(screen.getByRole("button", { name: "Reveal" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Show Password" })).toBeInTheDocument());
    expect(screen.queryByText("s3cretPassword")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show Password" }));
    expect(screen.getByText("s3cretPassword")).toBeInTheDocument();

    // Databases list with primary protection.
    expect(await screen.findByText("reports")).toBeInTheDocument();
    expect(screen.getByText("primary")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Drop acme_shop" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Drop reports" })).toBeInTheDocument();

    await user.type(screen.getByLabelText("New database"), "Analytics-2");
    expect(screen.getByLabelText("New database")).toHaveValue("analytics2");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/database/databases"))).toBe(true));
    expect(await screen.findByText("Database “analytics2” created.")).toBeInTheDocument();
  });
});

describe("DatabaseTab database browser", () => {
  afterEach(() => vi.unstubAllGlobals());

  const dbRoutes = {
    ...authedRoutes,
    "GET /runtimes": () => ({ body: runtimesFixture }),
    "GET /settings": () => ({ body: { publicHost: "" } }),
    "GET /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/database/databases": () => ({ body: { databases: ["acme_shop"] } }),
    "GET /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/database": () => ({
      body: { database: { type: "mariadb", version: "11", image: "mariadb:11", host: "database", port: 3306, database: "acme_shop", username: "acme_shop", hostPort: 0, injectedEnv: [], state: "running", volumeName: "v", volumeExists: true } },
    }),
  };

  it("opens Adminer in a new tab when the browser is enabled", async () => {
    const api = mockApi({
      ...dbRoutes,
      "GET /dbtool": () => ({ body: { enabled: true, running: false, image: "adminer:5", supported: ["mariadb", "mysql", "postgresql"] } }),
      "POST /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/dbtool": () => ({ body: { url: "/dbtool/?server=envoryx-acme-shop-database&username=acme_shop&db=acme_shop", server: "envoryx-acme-shop-database", username: "acme_shop", database: "acme_shop" } }),
    });
    const open = vi.fn(() => ({}) as Window);
    vi.stubGlobal("open", open);
    renderApp(<DatabaseTab project={withDb()} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Open database" }));
    await waitFor(() => expect(open).toHaveBeenCalledWith("/dbtool/?server=envoryx-acme-shop-database&username=acme_shop&db=acme_shop", "_blank", "noopener"));
    expect(api.calls.some((c) => c.method === "POST" && c.url.endsWith("/dbtool"))).toBe(true);
  });

  it("points to the settings when the browser is disabled", async () => {
    mockApi({ ...dbRoutes, "GET /dbtool": () => ({ body: { enabled: false, running: false, image: "adminer:5", supported: ["mariadb"] } }) });
    renderApp(<DatabaseTab project={withDb()} />);
    expect(await screen.findByText("Enable the database browser in Settings first.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open database" })).toBeDisabled();
  });

  it("switches between the primary and additional databases and adds another one", async () => {
    const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
    const project = makeProject({
      services: [
        ...makeProject().services,
        { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} },
        { kind: "db-analytics", variant: "postgresql", version: "18", image: "postgres:18", enabled: true, config: {} },
      ],
    });
    const info = (over: Record<string, unknown>) => ({
      body: { database: { name: "", service: "database", type: "mariadb", version: "11", image: "mariadb:11", host: "database", port: 3306, database: "acme_shop", username: "acme_shop", hostPort: 0, injectedEnv: [], state: "stopped", volumeName: "v", volumeExists: true, ...over } },
    });
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET /projects/${id}/database?db=analytics`]: () => info({ name: "analytics", service: "db-analytics", type: "postgresql", version: "18", host: "analytics", port: 5432, volumeName: "envoryx-acme-shop-db-analytics" }),
      [`GET /projects/${id}/database/snapshots?db=analytics`]: () => ({ body: { snapshots: [] } }),
      [`GET /projects/${id}/database/snapshots`]: () => ({ body: { snapshots: [] } }),
      [`GET /projects/${id}/database`]: () => info({}),
      [`PATCH /projects/${id}`]: () => ({ body: { project } }),
    });
    renderApp(<DatabaseTab project={project} />);
    const user = userEvent.setup();

    expect(await screen.findByRole("button", { name: /Primary/, pressed: true })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /analytics/ }));
    expect(await screen.findByText("envoryx-acme-shop-db-analytics")).toBeInTheDocument();
    expect(screen.getByText(/ANALYTICS_DB_\*/)).toBeInTheDocument();
    expect(api.calls.some((c) => c.url.endsWith("/database?db=analytics"))).toBe(true);

    await user.click(screen.getAllByRole("button", { name: "Add database" })[0]!);
    const name = await screen.findByLabelText("Name");
    await user.type(name, "legacy");
    await user.selectOptions(screen.getByLabelText("Type"), "mariadb");
    const buttons = screen.getAllByRole("button", { name: "Add database" });
    await user.click(buttons[buttons.length - 1]!);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toMatchObject({ databases: { legacy: { enabled: true, type: "mariadb" } } });
  });

  it("adds an external database after testing the connection", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /external/test": () => ({ body: { ok: true } }),
      [`PATCH ${P}`]: () => ({ body: { project: makeProject() } }),
    });
    renderApp(<DatabaseTab project={makeProject()} />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("radio", { name: "On an external server" }));
    expect(screen.queryByRole("checkbox", { name: /^Publish database port/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add database" })).toBeDisabled();
    await user.type(screen.getByLabelText("Host"), "host.docker.internal");
    await user.type(screen.getByLabelText("Port"), "3307");
    await user.type(screen.getByLabelText("Database"), "shop");
    await user.type(screen.getByLabelText("Username"), "shop_app");
    await user.type(screen.getByLabelText("Password"), "p@ss");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Connected.")).toBeInTheDocument();
    expect(api.calls.find((c) => c.url.endsWith("/external/test"))!.body).toEqual({ kind: "database", type: "mariadb", version: "11", host: "host.docker.internal", port: 3307, username: "shop_app", password: "p@ss", database: "shop" });

    await user.click(screen.getByRole("button", { name: "Add database" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      database: { enabled: true, type: "mariadb", version: "11", external: { host: "host.docker.internal", port: 3307, username: "shop_app", password: "p@ss", database: "shop" } },
    });
  });

  it("shows an external database without the container's controls and edits its connection", async () => {
    const external = makeProject({
      services: [
        ...makeProject().services,
        { kind: "database", variant: "mysql", version: "8.4", image: "mysql:8.4", enabled: true, config: { database: "shop", username: "shop_app", hostPort: 0, host: "db.example.com", port: 3306 } },
      ],
    });
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/database/databases`]: () => ({ body: { databases: ["shop", "another_app"] } }),
      [`GET ${P}/database`]: () => ({
        body: { database: { type: "mysql", version: "8.4", image: "mysql:8.4", host: "db.example.com", port: 3306, database: "shop", username: "shop_app", hostPort: 0, injectedEnv: ["DB_HOST"], state: "external", volumeName: "", volumeExists: false, external: true } },
      }),
      "POST /external/test": () => ({ status: 422, body: { error: { code: "validation_failed", message: "cannot connect" } } }),
      [`PATCH ${P}`]: () => ({ body: { project: external } }),
    });
    renderApp(<DatabaseTab project={external} />);
    const user = userEvent.setup();

    expect(await screen.findByText("db.example.com", { selector: "dd" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rotate password" })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: /^Publish port/ })).not.toBeInTheDocument();
    expect(screen.getByText("Connect your client to the server itself, at db.example.com:3306.")).toBeInTheDocument();
    // The server's databases are listed without the project running, never dropped.
    expect(await screen.findByText("another_app")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Drop another_app" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Edit connection" }));
    const host = screen.getByLabelText("Host");
    expect(host).toHaveValue("db.example.com");
    await user.clear(host);
    await user.type(host, "db2.example.com");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    // An empty password keeps the stored one.
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ database: { enabled: true, version: "8.4", external: { host: "db2.example.com", port: 3306, username: "shop_app", password: "", database: "shop" } } });

    await user.click(screen.getByRole("button", { name: "Remove connection" }));
    const dialog = (await screen.findByText(/the server and its data are not touched/)).closest("dialog")!;
    expect(within(dialog).queryByLabelText(/to confirm/)).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Remove connection" })).toBeEnabled();
  });
});
