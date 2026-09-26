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

describe("ServicesTab search engines", () => {
  const meilisearch = { kind: "meilisearch", version: "1.54", image: "getmeili/meilisearch:v1.54", host: "meilisearch", port: 7700, hostPort: 26010, webUiPort: 26010, injectedEnv: ["MEILISEARCH_API_KEY", "MEILISEARCH_HOST", "MEILISEARCH_KEY", "MEILISEARCH_URL"], state: "running", volumeName: "envoryx-acme-shop-meilisearch" };

  it("links the Meilisearch dashboard, keeps its port published and reveals the master key on request", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/extras`]: () => ({ body: { services: [meilisearch] } }),
      [`GET ${P}/storage`]: () => ({ status: 404, body: { error: { code: "not_found", message: "no storage" } } }),
      [`GET ${P}/meilisearch/credentials`]: () => ({ body: { credentials: { apiKey: "k3yk3yk3yk3yk3yk3yk3yk3yk3yk3yk3", url: "http://meilisearch:7700" } } }),
    });
    renderApp(<ServicesTab project={makeProject()} />);
    const user = userEvent.setup();

    expect(await screen.findByText("Dashboard")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /:26010/ })).toBeInTheDocument();
    // Meilisearch's port carries the dashboard: no publish checkbox on its card, only on
    // the cards offering Redis, Memcached, RabbitMQ, Typesense, OpenSearch and Ollama.
    expect(screen.getAllByLabelText("Publish port on the host")).toHaveLength(6);
    expect(screen.getByRole("button", { name: "Add Typesense" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add OpenSearch" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show master key" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/meilisearch/credentials"))).toBe(true));
    expect(await screen.findByText("Master key")).toBeInTheDocument();
  });

  it("shows OpenSearch Dashboards on the OpenSearch card and switches it off without touching the rest", async () => {
    const opensearch = { kind: "opensearch", version: "3.8", image: "opensearchproject/opensearch:3.8.0", host: "opensearch", port: 9200, hostPort: 0, webUiPort: 26012, injectedEnv: ["OPENSEARCH_HOST", "OPENSEARCH_PORT", "OPENSEARCH_SCHEME", "OPENSEARCH_URL"], state: "running", volumeName: "envoryx-acme-shop-opensearch", dashboards: { image: "opensearchproject/opensearch-dashboards:3.8.0", state: "running", health: "healthy" } };
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/extras`]: () => ({ body: { services: [opensearch] } }),
      [`GET ${P}/storage`]: () => ({ status: 404, body: { error: { code: "not_found", message: "no storage" } } }),
      [`PATCH ${P}`]: () => ({ body: { project: makeProject() } }),
    });
    renderApp(<ServicesTab project={makeProject()} />);
    const user = userEvent.setup();

    expect(await screen.findByRole("link", { name: /:26012/ })).toBeInTheDocument();
    const toggle = screen.getByRole("checkbox", { name: /^OpenSearch Dashboards/ });
    expect(toggle).toBeChecked();
    await user.click(toggle);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    const body = api.calls.find((c) => c.method === "PATCH")!.body as { opensearch: { enabled: boolean; version: string; exposePort: boolean; dashboards: boolean } };
    expect(body.opensearch).toEqual({ enabled: true, version: "3.8", exposePort: false, dashboards: false });
  });
});

describe("ServicesTab Ollama", () => {
  afterEach(() => vi.unstubAllGlobals());

  const ollama = { kind: "ollama", version: "0.34", image: "ollama/ollama:0.34.4", host: "ollama", port: 11434, hostPort: 0, injectedEnv: ["OLLAMA_BASE_URL", "OLLAMA_HOST", "OLLAMA_URL"], state: "running", health: "healthy" };
  const models = [{ name: "qwen3:8b", size: 5_200_000_000, modifiedAt: "2026-09-26T10:00:00Z", family: "qwen3", parameterSize: "8.2B", quantization: "Q4_K_M" }];

  it("lists the shared models, starts a download, shows its progress and deletes a model after asking", async () => {
    let pulls: unknown[] = [];
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/extras`]: () => ({ body: { services: [ollama] } }),
      [`GET ${P}/storage`]: () => ({ status: 404, body: { error: { code: "not_found", message: "no storage" } } }),
      [`GET ${P}/ollama/models`]: () => ({ body: { models, pulls } }),
      [`POST ${P}/ollama/models`]: () => {
        pulls = [{ model: "llama3.2", status: "pulling abc", completed: 1_000_000_000, total: 2_000_000_000, done: false, startedAt: "2026-09-26T12:00:00Z" }];
        return { status: 202, body: { pull: pulls[0] } };
      },
      [`DELETE ${P}/ollama/models`]: () => ({ status: 204 }),
    });
    renderApp(<ServicesTab project={makeProject()} />);
    const user = userEvent.setup();

    expect(await screen.findByText("qwen3:8b")).toBeInTheDocument();
    expect(screen.getByText("8.2B")).toBeInTheDocument();
    expect(screen.getByText("shared by all projects")).toBeInTheDocument();

    const input = screen.getByLabelText("Model");
    await user.type(input, "bad name");
    expect(screen.getByText("Not a model name, e.g. llama3.2 or qwen3:8b")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download" })).toBeDisabled();
    await user.clear(input);
    await user.type(input, "llama3.2");
    await user.click(screen.getByRole("button", { name: "Download" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST")).toBe(true));
    expect(api.calls.find((c) => c.method === "POST")!.body).toEqual({ model: "llama3.2" });
    const bar = await screen.findByRole("progressbar", { name: "Download of llama3.2" });
    expect(bar).toHaveAttribute("aria-valuenow", "50");

    await user.click(screen.getByRole("button", { name: "Delete qwen3:8b" }));
    expect(await screen.findByText(/every project with Ollama loses it/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(api.calls.find((c) => c.method === "DELETE")!.url).toBe(`/api/v1${P}/ollama/models/qwen3%3A8b`);
  });

  it("switches the GPU on and names the missing toolkit when Docker cannot", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/extras`]: () => ({ body: { services: [ollama] } }),
      [`GET ${P}/storage`]: () => ({ status: 404, body: { error: { code: "not_found", message: "no storage" } } }),
      [`GET ${P}/ollama/models`]: () => ({ body: { models: [], pulls: [] } }),
      [`PATCH ${P}`]: () => ({ status: 409, body: { error: { code: "gpu_unavailable", message: "Docker cannot hand GPUs to containers; install the NVIDIA Container Toolkit (on Unraid: the Nvidia Driver plugin) and restart Docker" } } }),
    });
    renderApp(<ServicesTab project={makeProject()} />);
    const user = userEvent.setup();

    const gpu = await screen.findByRole("checkbox", { name: /^Use the GPU/ });
    expect(gpu).not.toBeChecked();
    await user.click(gpu);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({ ollama: { enabled: true, version: "0.34", exposePort: false, gpu: true } });
    expect(await screen.findByText(/install the NVIDIA Container Toolkit/)).toBeInTheDocument();
  });

  it("asks to start the project before models can be managed", async () => {
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      [`GET ${P}/extras`]: () => ({ body: { services: [{ ...ollama, state: "exited", health: undefined }] } }),
      [`GET ${P}/storage`]: () => ({ status: 404, body: { error: { code: "not_found", message: "no storage" } } }),
    });
    renderApp(<ServicesTab project={makeProject()} />);
    expect(await screen.findByText("Start the project to download and manage models.")).toBeInTheDocument();
  });
});
