import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServicesTab } from "./ServicesTab";
import { authedRoutes, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";

const P = "/projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";
const memcached = { kind: "memcached", version: "1.6", image: "memcached:1.6-alpine", host: "memcached", port: 11211, hostPort: 0, injectedEnv: ["MEMCACHED_HOST", "MEMCACHED_PORT", "MEMCACHED_URL"], state: "running" };
const phpConfig = { memoryLimit: "256M", uploadMaxFilesize: "64M", postMaxSize: "64M", maxExecutionTime: 60, displayErrors: true, errorReporting: "E_ALL", extensions: ["opcache", "pdo_mysql"] };

function projectWithPhp() {
  const p = makeProject();
  return { ...p, services: p.services.map((s) => (s.kind === "php" ? { ...s, config: phpConfig } : s)) };
}

describe("ServicesTab", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("offers to switch on the PHP extension a service needs", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/extras`]: () => ({ body: { services: [memcached] } }),
      [`GET ${P}/storage`]: () => ({ status: 404, body: { error: { code: "not_found", message: "no storage" } } }),
      [`PATCH ${P}`]: () => ({ body: { project: projectWithPhp() } }),
    });
    renderApp(<ServicesTab project={projectWithPhp()} />);
    const user = userEvent.setup();

    expect(await screen.findByText(/PHP clients need the memcached extension/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Enable memcached" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    const body = api.calls.find((c) => c.method === "PATCH")!.body as { php: { version: string; config: { extensions: string[]; memoryLimit: string } } };
    expect(body.php.version).toBe("8.4");
    expect(body.php.config.extensions).toEqual(["memcached", "opcache", "pdo_mysql"]);
    expect(body.php.config.memoryLimit).toBe("256M");
  });

  it("adds a service together with its PHP extension and without a hint where nothing is missing", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/extras`]: () => ({ body: { services: [] } }),
      [`GET ${P}/storage`]: () => ({ status: 404, body: { error: { code: "not_found", message: "no storage" } } }),
      [`PATCH ${P}`]: () => ({ body: { project: projectWithPhp() } }),
    });
    renderApp(<ServicesTab project={projectWithPhp()} />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Add Redis" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    const body = api.calls.find((c) => c.method === "PATCH")!.body as { redis: { enabled: boolean }; php: { config: { extensions: string[] } } };
    expect(body.redis.enabled).toBe(true);
    expect(body.php.config.extensions).toEqual(["opcache", "pdo_mysql", "redis"]);
    // No service yet, so no card asks for an extension.
    expect(screen.queryByText(/PHP clients need/)).not.toBeInTheDocument();
  });
});
