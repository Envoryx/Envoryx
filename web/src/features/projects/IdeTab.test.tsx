import { screen } from "@testing-library/react";
import { IdeTab } from "./IdeTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

describe("IdeTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows SSH, Xdebug and database values ready to copy", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({
        body: {
          publicHost: "192.168.178.5", baseDomain: "test", forceHttps: false, proxy: { enabled: true, httpPort: 80, httpsPort: 443, inDocker: true, tls: true, address: "192.168.178.5" },
          ssh: { enabled: true, port: 2222, fingerprint: "SHA256:abc" }, projectsDir: "/projects", hostPath: { overrides: {}, detected: { "/projects": "/mnt/user/development" }, bareMetal: false },
        },
      }),
      [`GET /projects/${id}/database`]: () => ({ body: { database: { type: "mariadb", version: "11", host: "database", port: 3306, database: "shimly_api", username: "shimly_api", hostPort: 20003, injectedEnv: [], state: "running", volumeName: "v", volumeExists: true } } }),
      [`GET /projects/${id}/services/extra`]: () => ({ body: { services: [] } }),
      [`GET /projects/${id}/extras`]: () => ({ body: { services: [] } }),
    });
    const project = makeProject({ services: [...makeProject().services, { kind: "database", variant: "mariadb", version: "11", image: "mariadb:11", enabled: true, config: {} }] });
    renderApp(<IdeTab project={project} />);
    expect(await screen.findByText("ssh -p 2222 shimly-api@192.168.178.5")).toBeInTheDocument();
    expect(screen.getByText("/mnt/user/development/shimly-api → /var/www/html")).toBeInTheDocument();
    expect(await screen.findByText("jdbc:mariadb://192.168.178.5:20003/shimly_api")).toBeInTheDocument();
    expect(screen.getByText(/PhpProjectServersManager/)).toBeInTheDocument();
  });
});
