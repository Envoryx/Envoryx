import { screen } from "@testing-library/react";
import { IdeTab } from "./IdeTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const settings = {
  publicHost: "192.168.1.10", baseDomain: "test", forceHttps: false, proxy: { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true, address: "192.168.1.10" },
  ssh: { enabled: true, port: 2222, fingerprint: "SHA256:abc" }, projectsDir: "/projects", hostPath: { overrides: {}, detected: { "/projects": "/mnt/user/development" }, bareMetal: false },
};
const nodeService = { kind: "node", variant: "node", version: "24", image: "ghcr.io/envoryx/envoryx-node:24", enabled: true, config: { devServer: true, preset: "vite", port: 5173 } };
const webService = makeProject().services.find((s) => s.kind === "web")!;

describe("IdeTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("describes the Node interpreter and hides PHP-only rows for a Node-only project", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      [`GET /projects/${id}/extras`]: () => ({ body: { services: [] } }),
    });
    const project = makeProject({ services: [webService, nodeService], serves: "node", appService: "node" });
    renderApp(<IdeTab project={project} />);
    expect(await screen.findByText("ssh -p 2222 acme-shop@192.168.1.10")).toBeInTheDocument();
    expect(screen.getByText(/WebStorm: Settings/)).toBeInTheDocument();
    expect(screen.getByText("User")).toBeInTheDocument();
    expect(screen.getByText("/usr/local/bin/node")).toBeInTheDocument();
    expect(screen.getByText("envoryx-acme-shop-node")).toBeInTheDocument();
    expect(screen.queryByText("PHP path")).not.toBeInTheDocument();
    expect(screen.queryByText("User (Node)")).not.toBeInTheDocument();
    expect(screen.queryByText("Xdebug")).not.toBeInTheDocument();
  });

  it("names no session container for a static project", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      [`GET /projects/${id}/extras`]: () => ({ body: { services: [] } }),
    });
    const { appService: _php, ...base } = makeProject();
    const project = { ...base, services: [webService], serves: "static" as const };
    renderApp(<IdeTab project={project} />);
    expect(await screen.findByText(/no application container/)).toBeInTheDocument();
    expect(screen.queryByText(/envoryx-acme-shop-php/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Sessions run as the project owner/)).not.toBeInTheDocument();
  });

  it("offers explicit .php and .node users when both runtimes exist", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      [`GET /projects/${id}/extras`]: () => ({ body: { services: [] } }),
    });
    const project = makeProject({ services: [...makeProject().services, nodeService], serves: "php", appService: "php" });
    renderApp(<IdeTab project={project} />);
    expect(await screen.findByText("acme-shop.php")).toBeInTheDocument();
    expect(screen.getByText("acme-shop.node")).toBeInTheDocument();
    expect(screen.getByText("/usr/local/bin/php")).toBeInTheDocument();
    expect(screen.getByText("/usr/local/bin/node")).toBeInTheDocument();
    expect(screen.getByText("Xdebug")).toBeInTheDocument();
  });

  it("shows SSH, Xdebug and database values ready to copy", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({
        body: {
          publicHost: "192.168.1.10", baseDomain: "test", forceHttps: false, proxy: { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true, address: "192.168.1.10" },
          ssh: { enabled: true, port: 2222, fingerprint: "SHA256:abc" }, projectsDir: "/projects", hostPath: { overrides: {}, detected: { "/projects": "/mnt/user/development" }, bareMetal: false },
        },
      }),
      [`GET /projects/${id}/database`]: () => ({ body: { database: { type: "mariadb", version: "11", host: "database", port: 3306, database: "acme_shop", username: "acme_shop", hostPort: 20003, injectedEnv: [], state: "running", volumeName: "v", volumeExists: true } } }),
      [`GET /projects/${id}/services/extra`]: () => ({ body: { services: [] } }),
      [`GET /projects/${id}/extras`]: () => ({ body: { services: [] } }),
    });
    const project = makeProject({ services: [...makeProject().services, { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} }] });
    renderApp(<IdeTab project={project} />);
    expect(await screen.findByText("ssh -p 2222 acme-shop@192.168.1.10")).toBeInTheDocument();
    expect(screen.getByText("/mnt/user/development/acme-shop → /var/www/html")).toBeInTheDocument();
    expect(await screen.findByText("jdbc:mariadb://192.168.1.10:20003/acme_shop")).toBeInTheDocument();
    expect(screen.getByText(/PhpProjectServersManager/)).toBeInTheDocument();
  });
});
