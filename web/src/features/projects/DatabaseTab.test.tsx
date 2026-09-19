import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DatabaseTab } from "./DatabaseTab";
import { authedRoutes, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";

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
});
