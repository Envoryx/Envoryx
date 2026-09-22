import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { NewProjectPage } from "./NewProjectPage";
import { authedRoutes, makeProject, mockApi, renderApp, runtimesFixture } from "@/test/utils";
import type { Preview, RuntimesResponse } from "@/api/types";

/** The PHP fixture plus what a backend with Node-only support adds: Node templates and presets. */
const nodeRuntimesFixture: RuntimesResponse = {
  ...runtimesFixture,
  templates: [
    ...(runtimesFixture.templates ?? []).map((tpl) => ({ ...tpl, runtime: "php" as const })),
    { id: "vite", name: "Vite + React (TypeScript)", description: "Vite scaffold with React and TypeScript – dev server with HMR on the project URL.", docroot: "dist", requiresDatabase: false, runtime: "node", node: { devServer: true, preset: "vite", port: 5173, script: "dev" } },
    { id: "next", name: "Next.js (App Router, TypeScript)", description: "create-next-app with the App Router and TypeScript.", docroot: "", requiresDatabase: false, runtime: "node", node: { devServer: true, preset: "next", port: 3000, script: "dev" } },
  ],
  nodePresets: [
    { key: "vite", label: "Vite (Vue, React, Svelte, Laravel…)", port: 5173 },
    { key: "next", label: "Next.js", port: 3000 },
    { key: "nuxt", label: "Nuxt", port: 3000 },
    { key: "generic", label: "Other (HOST/PORT env only)", port: 5173 },
  ],
};

const emptyPreview: Preview = { slug: "acme-shop", path: "/projects/acme-shop", hostPath: "/x", httpPort: 20000, network: "n", containers: [], volumes: [], images: [], warnings: [] };

/** Records the preview body and answers with a plan derived from the request, like the backend does. */
function previewRoute(onBody: (b: Record<string, unknown>) => void, extra: Partial<Preview> = {}) {
  return (_u: string, init: RequestInit) => {
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    onBody(body);
    const serves = body.php ? "php" : (body.node as { devServer?: boolean } | undefined)?.devServer ? "node" : "static";
    return { body: { preview: { ...emptyPreview, serves, ...extra } } };
  };
}

describe("NewProjectPage wizard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("walks through the steps, previews and creates", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": () => ({
        body: {
          preview: {
            slug: "acme-shop",
            path: "/projects/acme-shop",
            hostPath: "/mnt/user/development/acme-shop",
            httpPort: 20000,
            network: "envoryx-acme-shop",
            containers: [
              { service: "php", name: "envoryx-acme-shop-php", image: "ghcr.io/envoryx/envoryx-php:8.4", ports: [], mounts: ["/mnt/user/development/acme-shop → /var/www/html"] },
              { service: "web", name: "envoryx-acme-shop-web", image: "caddy:2-alpine", ports: ["20000 → 80/tcp"], mounts: [] },
            ],
            volumes: [],
            images: ["ghcr.io/envoryx/envoryx-php:8.4", "caddy:2-alpine"],
            warnings: ["image ghcr.io/envoryx/envoryx-php:8.4 will be pulled on first start"],
          },
        },
      }),
      "POST /projects": () => ({ status: 201, body: { project: makeProject() } }),
    });
    renderApp(
      <Routes>
        <Route path="/projects/new" element={<NewProjectPage />} />
        <Route path="/projects/:id" element={<h1>Detail</h1>} />
      </Routes>,
      { route: "/projects/new" },
    );
    const user = userEvent.setup();

    const cont = async () => user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText("Project name")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
    await user.type(screen.getByLabelText("Project name"), "Acme Shop");
    expect(screen.getByText("Identifier: acme-shop")).toBeInTheDocument();
    await cont();

    // Runtime step: versions come from the API, not the UI.
    const version = await screen.findByLabelText("PHP version");
    expect(version).toHaveValue("8.4");
    await user.selectOptions(version, "8.3");
    expect(screen.getByLabelText(/gd/)).toBeDisabled();
    await user.click(screen.getByLabelText(/Enable Node\.js/));
    expect(screen.getByLabelText("Node.js version")).toHaveValue("24");
    await cont();

    expect(await screen.findByLabelText("Web server")).toHaveValue("caddy");
    await user.selectOptions(screen.getByLabelText("Web server"), "apache");
    expect(screen.getByLabelText("Version")).toHaveValue("2.4");
    expect(screen.getByText(/honours \.htaccess/)).toBeInTheDocument();
    await cont();
    expect(await screen.findByText("Database")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "MariaDB" }));
    expect(screen.getByLabelText("Version")).toHaveValue("11");
    await user.click(screen.getByLabelText(/Publish database port/));
    await user.click(screen.getByLabelText(/^Mailpit/));
    await cont();
    await user.click(await screen.findByRole("button", { name: "Add variable" }));
    await user.type(screen.getByLabelText("Variable name"), "app_env");
    expect(screen.getByLabelText("Variable name")).toHaveValue("APP_ENV");
    await user.type(screen.getByLabelText("Variable value"), "local");
    await cont();

    expect(await screen.findByText("envoryx-acme-shop-php")).toBeInTheDocument();
    expect(screen.getByText("envoryx-acme-shop-web")).toBeInTheDocument();
    expect(screen.getByText(/will be pulled/)).toBeInTheDocument();
    const preview = api.calls.find((c) => c.url.endsWith("/projects/preview"))?.body as Record<string, unknown>;
    expect(preview).toMatchObject({ name: "Acme Shop", path: "acme-shop", docroot: "public", php: { version: "8.3" }, node: { version: "24" }, database: { type: "mariadb", version: "11", exposePort: true }, mailpit: {}, env: [{ key: "APP_ENV", value: "local" }] });

    await user.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Detail" })).toBeInTheDocument());
    const create = api.calls.find((c) => c.method === "POST" && c.url.endsWith("/projects"));
    expect(create?.body).toMatchObject({ name: "Acme Shop", start: true, createStarter: true, web: { type: "apache", version: "2.4" } });
  });

  it("selecting a template presets docroot and database and disables git", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": (_u, init) => {
        previewBody = JSON.parse(init.body as string);
        return { body: { preview: { slug: "blog", path: "/projects/blog", hostPath: "/x", httpPort: 20000, network: "n", containers: [], volumes: [], images: [], warnings: [] } } };
      },
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Blog");
    await user.click(screen.getByRole("radio", { name: /WordPress/ }));
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    expect(screen.getByLabelText("Repository URL")).toBeDisabled();
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText("Latest WordPress");
    expect(previewBody).toMatchObject({ template: "wordpress", docroot: "", database: { type: "mariadb" } });
  });

  it("creates a Node-only project: no php key, dev server on, no starter", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b), {
        devHostname: "acme-shop-dev.test",
        containers: [
          { service: "node", name: "envoryx-acme-shop-node", image: "ghcr.io/envoryx/envoryx-node:24", ports: ["20001 → 5173/tcp"], mounts: [] },
          { service: "web", name: "envoryx-acme-shop-web", image: "caddy:2-alpine", ports: [], mounts: [] },
        ],
        warnings: ['the dev server runs "npm run dev" but nothing creates a package.json – pick a Node template, clone a repository or scaffold from the Node terminal; until then the container waits'],
      }),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Acme Shop");
    expect(screen.getByRole("radio", { name: "PHP application" })).toBeChecked();
    // Templates follow the stack: PHP ones disappear, Node ones show up.
    expect(screen.getByRole("radio", { name: /WordPress/ })).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    expect(screen.queryByRole("radio", { name: /WordPress/ })).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Vite \+ React/ })).toBeInTheDocument();
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    expect(screen.getByText(/Not used while the dev server serves the app/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));

    // Runtimes step: the Node card comes first with the dev server preselected; PHP is off.
    expect(await screen.findByLabelText(/Enable Node\.js/)).toBeChecked();
    expect(screen.getByLabelText(/Enable PHP/)).not.toBeChecked();
    expect(screen.getByLabelText(/Run a dev server/)).toBeChecked();
    expect(screen.getByLabelText("Framework preset")).toHaveValue("vite");
    expect(screen.getByText(/answers on the project URL; <slug>-dev/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByText(/The web server is part of every project/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/SPA fallback/)).not.toBeInTheDocument();
    for (let i = 0; i < 3; i++) await user.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByText("Node dev server (the HTTP port stays unpublished)")).toBeInTheDocument();
    // No proxy settings in this test: the URL row shows the direct port, which must be the
    // node container's published host port – the web HTTP port (20000) has nothing listening.
    expect(screen.getByText("http://localhost:20001")).toBeInTheDocument();
    expect(screen.queryByText(/:20000/)).not.toBeInTheDocument();
    expect(screen.getByText(/nothing creates a package.json/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/Create starter/)).not.toBeInTheDocument();
    expect(screen.queryByText("Dev server URL")).not.toBeInTheDocument();
    expect(previewBody).not.toHaveProperty("php");
    expect(previewBody).toMatchObject({ docroot: "", createStarter: false, node: { version: "24", devServer: true, preset: "vite", port: 5173, script: "dev" } });
  });

  it("a Node template presets the dev server and docroot from its defaults", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b)),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Acme Shop");
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    await user.click(screen.getByRole("radio", { name: /Next\.js/ }));
    expect(screen.getByLabelText("Repository URL")).toBeDisabled();
    await user.click(screen.getByRole("radio", { name: /Vite \+ React/ }));
    expect(screen.getByLabelText("Document root")).toHaveValue("dist");
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText(/Vite scaffold with React/);
    expect(previewBody).toMatchObject({ template: "vite", docroot: "dist", node: { devServer: true, preset: "vite", port: 5173, script: "dev" } });
  });

  it("switching the stack drops a template of the other runtime", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": (_u, init) => {
        const body = JSON.parse(init.body as string) as Record<string, unknown>;
        previewBody = body;
        if (body.template === "wordpress" && !body.php) return { status: 422, body: { error: { code: "validation_failed", message: "invalid input: template wordpress needs PHP" } } };
        return { body: { preview: { ...emptyPreview, serves: "node" } } };
      },
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Blog");
    await user.click(screen.getByRole("radio", { name: /WordPress/ }));
    expect(screen.getByLabelText("Document root")).toHaveValue("");
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    expect(screen.getByRole("radio", { name: /^Blank/ })).toBeChecked();
    expect(screen.getByLabelText("Repository URL")).toBeEnabled();
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText("Node dev server (the HTTP port stays unpublished)");
    expect(screen.queryByText(/needs PHP/)).not.toBeInTheDocument();
    expect(previewBody).not.toHaveProperty("template");
    expect(previewBody).not.toHaveProperty("php");
  });

  it("static site: starter index.html and the SPA fallback", async () => {
    let previewBody: Record<string, unknown> | undefined;
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: nodeRuntimesFixture }),
      "POST /projects/preview": previewRoute((b) => (previewBody = b)),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Docs");
    await user.click(screen.getByRole("radio", { name: "Static site" }));
    // Only Blank is left to start from.
    expect(screen.getAllByRole("radio", { name: /Blank|WordPress|Laravel|Vite|Next/ })).toHaveLength(1);
    expect(screen.getByText(/Build output served by the web server/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText(/Enable PHP/)).not.toBeChecked();
    expect(screen.getByLabelText(/Enable Node\.js/)).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("The web server serves static files from the document root.")).toBeInTheDocument();
    expect(screen.getByText(/Caddy serves the document root statically/)).toBeInTheDocument();
    await user.click(screen.getByLabelText(/SPA fallback to index.html/));
    for (let i = 0; i < 3; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText(/Create starter index\.html/)).toBeChecked();
    expect(previewBody).not.toHaveProperty("php");
    expect(previewBody).not.toHaveProperty("node");
    expect(previewBody).toMatchObject({ createStarter: true, docroot: "", web: { type: "caddy", version: "2", spaFallback: true } });
  });

  it("leaving the Node stack turns the dev server default back off", async () => {
    mockApi({ ...authedRoutes, "GET /runtimes": () => ({ body: nodeRuntimesFixture }) });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "App");
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    await user.click(screen.getByRole("radio", { name: "PHP application" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    // Node as a toolchain next to PHP starts without a dev server, like a fresh PHP flow.
    await user.click(await screen.findByLabelText(/Enable Node\.js/));
    expect(screen.getByLabelText(/Run a dev server/)).not.toBeChecked();
  });

  it("turning the dev server off on the Node stack suggests dist as the document root", async () => {
    mockApi({ ...authedRoutes, "GET /runtimes": () => ({ body: nodeRuntimesFixture }) });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "App");
    await user.click(screen.getByRole("radio", { name: "Node.js application" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByLabelText(/Run a dev server/));
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(await screen.findByLabelText("Document root")).toHaveValue("dist");
  });

  it("shows validation errors from the preview", async () => {
    mockApi({
      ...authedRoutes,
      "GET /runtimes": () => ({ body: runtimesFixture }),
      "POST /projects/preview": () => ({ status: 422, body: { error: { code: "validation_failed", message: "invalid input: path must stay inside the projects directory" } } }),
    });
    renderApp(<NewProjectPage />, { route: "/projects/new" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Project name"), "Evil");
    await user.type(screen.getByLabelText("Project directory"), "../etc");
    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText(/must stay inside/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create project" })).toBeDisabled();
  });
});
